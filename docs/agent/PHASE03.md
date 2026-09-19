# Phase 0.3 — Essential torrent actions (record)

Tracking: GitHub issue #5; branch `feat/phase03-essential-actions`.
Status: MERGED. Date: 2026-09-19.
Base: main @ 7b2a914 (Phase 0.2 merged at 3e090d5).

## Final merge record (2026-09-19)

- PR #6 **MERGED** into `main` via merge commit
  `fc8615f0996dd41b4df6192db7a098655338e6a3`
  (normal merge method, matching the PR #2/#4 convention; no
  squash/rebase/force-push).
- Final accepted head: `0061926588a304fccb947f74e214906608cb1e68`
  (pre-merge verification: OPEN, base `main` @ `7b2a914`, MERGEABLE/CLEAN,
  8 commits, 0 review threads, no new commits, no failing checks,
  head SHA identical to the externally approved head).
- External review verdict on head `0061926`: APPROVE FOR MERGE.
- Issue #5 CLOSED (COMPLETED) by the merge.
- Local `main` fast-forwarded to `fc8615f`, tree clean; merged local
  branch deleted.

## Delivered

```
Panel (QML, presentation only)
  intents: torrent.pause / resume / add / remove(+delete_files)
  confirmations (remove / remove+delete files distinct, urgent) + results
    ↓ IPC v1.2 (ADR-0006: staged mutations, strict schemas, ref replay)
internal/mutate (validation vs committed state, bounded submission,
  version-gated endpoint selection, ref ring, state-derived results)
    ↓
internal/qbittorrent (stop/start [pause/resume legacy], torrents/add,
  torrents/delete with EXPLICIT deleteFiles; one hash, never `all`)
    ↓
qBittorrent WebAPI 2.15.1 (live-verified semantics, docs/QBITTORRENT.md)
```

- Request accepted ≠ mutation confirmed: acceptance and confirmation are
  separate stages; confirmation derives exclusively from committed sync
  state (qBittorrent mutation endpoints return 200-empty in all
  scenarios — the HTTP response is never treated as per-torrent truth).
- `delete_files` is a required boolean at every layer; ambiguity is a
  protocol violation (connection closes); the adapter never relies on a
  backend default and never infers intent.
- Replay safety: completed refs return their recorded terminal outcome —
  a retried remove can never execute twice. Per-process limit documented
  (not durable across daemon restarts).
- Live mutation validation used ONLY a disposable random-infohash magnet
  (never resolvable — no metadata, no files) removed with BOTH
  deleteFiles variants; user's real torrent count verified 3 → 3 around
  every live run.

## Research highlights (docs/QBITTORRENT.md, mutation section)

- stop/start required on qBittorrent 5.x — pause/resume endpoints were
  REMOVED in 5.0 (live 404 + source-verified across release-5.0.0…5.2.3);
  adapter falls back to pause/resume below WebAPI 2.11.0 (fixture-tested
  legacy path).
- torrents/add on ≥ 5.2.0 returns structured JSON with
  added_torrent_ids; the torrent object exists immediately; duplicates
  and malformed magnets are refused with 409 (live-verified).
- torrents/delete is 200-empty always; removal reconciles through
  sync/maindata torrents_removed (live-verified).

## Evidence matrix (executed 2026-09-19)

| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` / `gofmt -l .` | PASS |
| `go test -race ./...` (config/ipc/mutate/qbittorrent/state) | PASS |
| Harness validation (82/82) + guard hook (39/39) | PASS |
| `omarchy plugin validate plugins/local.omatorrent` | PASS |
| `tools/test_quickshell.sh` incl. v1.2 lifecycle (10 stages, disposable torrent) | PASS |
| Live mutation research probes (disposable magnet + no-op unknown hashes) | PASS (user torrents untouched, 3 → 3) |
| Daemon under systemd: 0.3.0-phase03 start, sync, probe | PASS |
| Shell restart → new panel loads, zero QML journal errors | PASS |
| Panel live: rows, hover actions, +, header | PASS (screenshot) |
| Panel degraded: daemon stopped → truthful banner, rows retained, actions hidden | PASS (screenshot) |
| Panel recovery: daemon restarted → resubscribe + full snapshot | PASS (screenshot) |
| Journal audit across open/degraded/recovery cycle | PASS (zero OmaTorrent errors) |
| Secret scan | PASS |
| Confirm-strip / add-input visual appearance | NOT OBSERVABLE (no input-synthesis tool on this host; logic covered by smoke + schema tests; layouts reuse validated components) |
| Destructive remove-with-files on a torrent WITH real files | NOT RUN (no safe disposable target with files exists; fixture + no-file disposable + contract enforcement cover the path) |
| Legacy (pre-2.11.0) backend paths live | NOT RUN (single 5.2.3 backend; fixture-tested) |

## Bugs caught by validation

- Live smoke stage 3 exposed: add-replay matched on hash (absent on add
  requests) instead of the magnet URL → spurious ref_conflict. Fixed in
  internal/mutate (matches() now compares URL for add), regression test
  added (TestReplayAddMatchesOnURL).

## Review verdicts (all three ran after implementation, implementer excluded)

- **Architecture: APPROVE-WITH-NOTES.** All seven invariants verified with
  evidence (QML purity via vocabulary audit; import graph via go list —
  internal/ipc has zero internal deps; confirmation exclusively from
  committed state; no optimistic-authoritative QML; purely additive IPC;
  no Transmission coupling / no speculative abstraction; ADR-0006
  implemented clause by clause). 4 MINOR findings + 4 notes, all addressed
  in the fix round below.
- **Security: PASS-WITH-FINDINGS.** All 11 mandated focus items
  CONFIRMED-SAFE with file:line evidence (destructive boolean chain
  end-to-end, replay ring, concurrent same-ref, malformed inputs, payload
  bounds, no shell execution, no paths, no secret surfaces, log content,
  DoS bounds, retry behavior, anti-reflection); no BLOCKER/MAJOR. 3 MINOR
  + 4 INFO findings, addressed below.
- **QA: READY-FOR-REVIEW.** Every gate independently reproduced with
  `-count=1` (no cached evidence): gofmt/vet/build, full race suite (109
  PASS), targeted mutation tests 6/6, harness 82/82, guard 39/39, plugin
  validate, full live smoke 10/10 stages with disposable torrent and
  real-count 3 → 3, daemon active on 0.3.0-phase03, 30-min journal clean.
  Zero defects, zero flakes, no material discrepancies vs this record.

## Review fix round (2026-09-19, applied and re-validated)

- [arch-1 / sec-F1] Strict key sets restored for pre-v1.2 types:
  hash/url/delete_files/ref on hello/health/system.status/
  torrent.subscribe ⇒ invalid_message (TestPreV12TypesRejectMutationFields).
- [sec-F2] Registration race closed structurally: ref lookup (in-flight
  AND completed ring) now happens in the same critical section as
  registration (lookupRefLocked), so a ref that completed while another
  Submit validated can never re-register and re-execute.
- [arch-note6 / sec-F3] Result pump re-arms: a dropped source
  resubscribes after a bounded pause (logged) instead of silently dying
  for the process lifetime.
- [arch-2] Replayed-result wire shape (mutation.result WITH id)
  documented in docs/IPC.md + ADR-0006 amendment; contract example
  mutation-result-replay.txt + anti-drift test.
- [arch-3] Version-gating comment/docs corrected: empty (unprobed)
  version → modern endpoints; present-but-unparseable → legacy; both
  wrong guesses fail visibly (docs/QBITTORRENT.md adapter rule 5).
- [arch-4] Panel pending overlay now clears on EVERY stage-1 rejection
  (previously stuck on backend_unavailable/backend_rejected/busy/
  ref_conflict); addMagnet() guards on the same presentation-safe
  validation as the button (a schema-invalid frame would close the
  connection).
- [sec-F5] Advisory early busy-cap check before the O(N) state clone
  (spam no longer amplifies memory bandwidth).
- [sec-F6] http.Client refuses redirects (ErrUseLastResponse): a 302 can
  no longer silently convert a mutating POST to a GET.
- [arch-note7] Cross-reference comment on the duplicated 40/64-hex regex.
- [arch-note8] Stale manager_test.go reference in docs/TESTING.md fixed.
- [sec-F4 / arch-note5] Documented residuals in ADR-0006: converged
  in-flight replay gets no terminal frame if the original fails stage 1;
  theoretical result-before-accepted enqueue ordering (clients treat
  pushes and responses as independent id-keyed streams).
- [sec-F7] Documented as-is: >2048-byte magnet is a schema violation
  (invalid_message, connection closes) — the panel pre-validates so it
  is unreachable from the shipped UI.

Re-validation after the fix round: gofmt/vet clean; `go test -race
-count=1 ./...` PASS; daemon redeployed (restart, active); live smoke
re-run 10/10 PASS with real count 3 → 3; shell restarted with the fixed
panel — journal clean, panel re-verified visually.

## External review round (2026-09-19, on PR #6 head f4a935d)

One accepted finding + one documentation-integrity finding:

1. **result-before-accepted client race (FIXED).** ADR-0006 permits a
   `mutation.result` push to arrive before the matching
   `mutation.accepted` response. The panel's inline handler correlated
   by action+hash; for `torrent.add` (no hash until acceptance) the
   early result was consumed with no pending entry existing, then the
   late `accepted` CREATED one — a potentially stuck "adding…" overlay.
   Fix: the correlation moved into a pure presentation-side state
   machine, `plugins/local.omatorrent/MutationClient.js` — pending
   entries correlate by daemon mutation id; unknown-id terminal pushes
   are buffered in a bounded (16, FIFO-evicted) early-result map;
   `accepted` consumes an existing early result and creates NO overlay;
   matching is strictly by mutation id (foreign ids never cross-resolve);
   duplicate/late pushes are harmless; reset clears everything. The
   panel consumes it; the deterministic ordering tests import the SAME
   file (real logic, not a copy).
2. **"exactly once" delivery claim (CORRECTED).** The result pump can
   briefly resubscribe; a result published in that gap is not replayed.
   Chosen remedy (reviewer option B): the documented guarantee is now
   "at most once per live delivery subscription — clients must not
   depend on pushes", and the reference client self-heals with a
   same-ref watchdog (12 s): pause/resume/add pendings are re-sent with
   the SAME ref (daemon replays the recorded outcome, zero backend
   execution); `torrent.remove` is NEVER automatically re-sent — its
   overlay escalates to the ambiguity banner and the row settles via
   the state stream. ADR-0006 + docs/IPC.md updated accordingly.

Harness fix surfaced by re-validation: smoke refs are now run-unique
(fixed refs correctly collided with the daemon's per-process ref ring
across runs → ref_conflict — correct daemon behavior, harness bug).

New tests: deterministic ordering battery in tools/test_quickshell.sh
(both legal orders × pause/resume/remove/add, the add stuck-overlay
regression, reset-clears-early-buffer, post-reset stray accepted,
foreign-id isolation, duplicate push, FIFO bound, watchdog replay
settlement) driving the real MutationClient.js; Go wire test
TestResultPublishedDuringSubmitBothFramesDelivered (server delivers
both frames in either order, connection stays healthy).

Destructive safety unchanged: delete_files required boolean, no
defaults, one hash, never `all`, ref-replay protection, no automatic
destructive retry with a fresh ref, committed state as source of truth.

### Re-review verdicts (head 7e1c53f; all six mandated points verified)

- **Architecture: APPROVE-WITH-NOTES.** All six points VERIFIED with
  file:line evidence (order-independence by construction, add
  early-result trace, bounds/reset, delivery-guarantee accuracy,
  watchdog/never-remove, docs match). Notes: rename the internal
  `deleteFiles` record field (qB form-field name reserved for
  daemon-side code) — APPLIED (now `delFiles`); preserve the queried
  latch across add re-registration — APPLIED; state the ring bound at
  the watchdog sentence — APPLIED.
- **Security: PASS-WITH-FINDINGS.** All six points CONFIRMED-SAFE,
  verified by an adversarial node harness against the real JS (flood
  100k pushes → buffer stays ≤ 16; malformed frames → zero throws) and
  by code audit. One LOW defense-in-depth finding: an id-less push
  could correlate via undefined === undefined — unreachable (daemon
  always emits the id; panel type-guards), hardening APPLIED +
  regression test added. INFO: watchdog double-loss staleness
  (self-healing, documented tradeoff); ring-bound wording — APPLIED.
- **QA: READY-FOR-REVIEW.** All 10 checks reproduced on 7e1c53f,
  including two back-to-back live smokes (ordering battery + 10/10
  mutation stages, real count 3 → 3, no ref_conflict on a warm ring),
  full race suite, harness 82/82, guard 39/39, plugin validate (with
  negative controls), git hygiene (7 commits), daemon journal clean,
  real-file import cross-check. Zero defects, zero flakes.

## Known limitations

- Ref deduplication is per-daemon-process; a daemon restart forgets
  completed refs (client obligations forbid auto-replay of destructive
  refs across restarts).
- Base32 btih magnets are rejected (invalid_url) — hex-only in 0.3.
- mutation.result timeout is ambiguous by design; the panel surfaces it
  as "result unknown" rather than failure.
- Panel pending overlay is per-hash; a second mutation on the same hash
  before the first result replaces the overlay (cosmetic; results still
  reconcile correctly).
- Legacy pause/resume path is fixture-tested only (no 4.x backend).
