# ADR-0006: IPC v1.2 — staged torrent mutation contract

STATUS: ACCEPTED (2026-09-19, Phase 0.3 design; ADR-level per AGENTS.md —
adds message types and the protocol's first backend-mutating surface).

## CONTEXT

Phase 0.3 adds the first mutations: pause/resume, add magnet, remove
(with an explicit delete-files distinction). Live research
(docs/QBITTORRENT.md, mutation section) established the constraints:

- qBittorrent mutation endpoints return **200 with an empty body in all
  scenarios** — including unknown hashes and idempotent no-ops. The HTTP
  response carries NO per-torrent truth.
- `torrents/add` acceptance is asynchronous in the general case (the
  torrent object appears promptly, but legacy backends answer plain
  `Ok.` and duplicates are indistinguishable there; modern backends
  answer structured JSON and reject duplicates with 409).
- Therefore the ONLY truthful confirmation of any mutation is the
  daemon's committed sync state — the same source that already drives
  the read path (ADR-0005).
- Destructive actions (remove with files) must never be repeated by
  accident through retries or reconnect ambiguity, and the client must
  never present optimistic state as success.

## DECISION

Extend IPC v1 to **v1.2** with four request types and three server frame
types. No existing shape changes; clients that never send mutation
requests see no difference (strict-compatibility rule from ADR-0004
holds). Core principle: **request accepted ≠ mutation confirmed** —
acceptance and confirmation are separate stages, and confirmation is
derived exclusively from committed state.

### Requests (after hello; strict exact key sets)

```json
{"type":"torrent.pause","id":N,"hash":"…40/64 hex…","ref":"…"}
{"type":"torrent.resume","id":N,"hash":"…","ref":"…"}
{"type":"torrent.add","id":N,"url":"magnet:?xt=urn:btih:…","ref":"…"}
{"type":"torrent.remove","id":N,"hash":"…","delete_files":false,"ref":"…"}
```

- `ref` is a client-generated idempotency key (1–128 chars of
  `[A-Za-z0-9._:-]`), unique per mutation ATTEMPT. It enables the
  daemon-side replay handling below. It is never echoed in responses.
- `delete_files` is a REQUIRED JSON boolean with no default. A missing,
  non-boolean, or otherwise ambiguous value is `invalid_message`
  (connection closed): destructive ambiguity is a protocol violation,
  never a fallback. The daemon always forwards the intent explicitly to
  the backend — it never relies on a backend default, never infers
  delete-files intent, and never upgrades `false` to `true`.
- `hash` must be 40/64 hexadecimal characters at parse time.
- `url` must be a string of ≤ 2048 bytes starting with `magnet:`
  (structural magnet validation — scheme, `xt` urn, hex btih — happens
  in the mutation service and rejects with `invalid_url` WITHOUT closing
  the connection: a pasted non-magnet link is a user mistake, not a
  protocol violation).

### Stage 1 — request response (synchronous, ≤ 5 s)

`{"type":"mutation.accepted","protocol":1,"id":N,"mutation":M,"action":"torrent.pause","hash":"…"}`

The request passed validation against the daemon's committed state AND
the backend acknowledged the submission (HTTP 2xx). `mutation` is a
daemon-global monotonically increasing integer id; `action` echoes the
request type; `hash` is the target infohash (for `torrent.add` it is the
infohash the daemon parsed from the magnet and cross-checked against the
backend's echo on ≥ 5.2.0 backends).

`{"type":"mutation.rejected","protocol":1,"id":N,"code":"…"}` — no
mutation was performed (or the backend refused it). Deterministic codes:

| Code | Meaning |
|---|---|
| `stale_torrent` | hash is not in the daemon's committed state (ghost row, already removed, wrong identity) |
| `invalid_url` | magnet failed daemon-side structural validation |
| `duplicate` | `torrent.add` — hash already present in committed state |
| `backend_rejected` | backend refused the submission (e.g. add 409 not explained by state) |
| `backend_unavailable` | backend down (sync state degraded) or submission transport failure |
| `busy` | daemon-wide in-flight mutation cap (4) exceeded |
| `ref_conflict` | `ref` replayed with a different action/parameters than recorded |

Rejections do NOT close the connection and never echo hash/url/ref
(anti-reflection). They are semantic refusals, not protocol errors.

### Stage 2 — terminal result (pushed, bounded window ≤ 10 s)

`{"type":"mutation.result","protocol":1,"mutation":M,"action":"…","hash":"…","status":"confirmed|timeout"}`

`confirmed` — the committed sync state observed the intent within the
window: pause ⇒ state `paused`; resume ⇒ present and not `paused`; add ⇒
hash present; remove ⇒ hash absent. `timeout` — the window elapsed
without confirming evidence: the outcome is AMBIGUOUS and is surfaced as
such; the committed state remains the only authority and the client must
not auto-retry destructive actions. Result frames are delivered exactly
once per mutation, on every connection that has issued at least one
mutation request (pure status clients such as the bar widget never
receive them), ordered after any prior frames on that connection.

### Replay and retry rules (fail-safe, non-durable)

- The daemon keeps in-flight refs plus a ring of the last 64 completed
  refs with their terminal outcome (bounded memory).
- Replayed `ref` still in flight ⇒ `mutation.accepted` with the SAME
  mutation id; no second backend call; the result follows once.
- Replayed `ref` already completed ⇒ one terminal frame: the recorded
  `mutation.rejected` code or the recorded `mutation.result` status. No
  second backend call. A same-hash remove can therefore never execute
  twice through a retry.
- `ref_conflict` is returned when the replay's action/parameters differ
  from the recorded attempt.
- LIMIT (documented, deliberate): deduplication is per-daemon-process.
  After a daemon restart a replayed ref executes again; clients must
  never auto-replay destructive refs across daemon restarts — a fresh
  user action is required. Durable dedup waits for the storage phase.
- pause/resume are naturally idempotent (live-verified no-op 200s,
  docs/QBITTORRENT.md); the client MAY re-issue them with a NEW ref
  after a timeout. `torrent.add` re-issue is safe on modern backends
  (duplicate ⇒ 409 ⇒ `duplicate` rejection); on legacy backends the
  client must check state first. `torrent.remove` is NEVER retried
  blindly: after a timeout the client re-derives truth from state and a
  re-remove requires a fresh user confirmation.

### Concurrency and abuse bounds

- Mutation handlers run inline on the connection read loop: at most one
  mutation in flight per connection by construction (lockstep).
- A daemon-wide semaphore caps concurrent submissions at 4; excess
  requests get `busy`. Snapshot/delta delivery is unaffected (separate
  goroutines/queues).
- Submission timeout 5 s (adapter HTTP timeout); reconcile window 10 s.

### Ownership (AGENTS.md invariants)

All qBittorrent mutation vocabulary (`stop`/`start` vs `pause`/`resume`
by WebAPI version, `hashes` form fields, `deleteFiles`, 409 semantics)
stays inside `internal/qbittorrent` + `internal/mutate`. QML expresses
INTENT (`torrent.pause`), renders confirmations and results, and never
treats optimistic state as authoritative.

## CONSEQUENCES

- The IPC server performs blocking backend I/O during request handling
  for the first time (bounded 5 s, lockstep discipline) — acceptable;
  health/status remain cache-served and pushes are queue-driven.
- The daemon now mutates the backend; SECURITY.md's threat surface grows
  accordingly (reviewed there).
- The QML panel needs a pending-overlay pattern distinct from committed
  state, plus confirmation flows for both removal variants.
- Versioning: hello still reports protocol 1; v1.2 is a compatible
  extension. A future incompatible change bumps to 2.

## ALTERNATIVES

- Synchronous single-response mutations (result inside the request
  response): blocks up to the reconcile window, conflates acceptance
  with confirmation, cannot express add's async acceptance — rejected.
- Optimistic UI with eventual correction: violates the truthfulness
  invariant (UI falsely reporting success is an accepted risk) —
  rejected.
- Durable deduplication store: out of scope before the storage phase;
  documented per-process limit chosen instead — deferred.
- Client-only idempotency: cannot survive reconnect ambiguity (the
  client cannot know whether the daemon received/executed the request) —
  insufficient, hence daemon-side ref handling.
- Single `torrent.mutate` request with an embedded action field:
  weaker schema (per-action key sets like `delete_files` would become
  conditional), more validation ambiguity — rejected.

## EVIDENCE/SOURCES

- docs/QBITTORRENT.md mutation section — live-confirmed semantics on
  qbittorrent-nox 5.2.3 / WebAPI 2.15.1 (200-empty responses, immediate
  appearance, 409 duplicates, torrents_removed reconciliation,
  pause/resume removal in 5.x).
- docs/IPC.md v1.2 section (normative text).
- Phase 0.2 review precedents: one-in-flight client discipline, bounded
  queues, anti-reflection error frames (ADR-0004/0005).
