# ADR-0007: IPC v1.3 — daemon-side dashboard aggregates

STATUS: ACCEPTED (2026-09-19, Phase 0.4 design; ADR-level per AGENTS.md —
adds message types and a new read surface).

## CONTEXT

Phase 0.4 adds a large-format dashboard overlay. The dashboard needs
current population counts (by normalized state), current aggregate
sizes, and a small "transferring now" list — none of which exist as a
wire surface today.

Two implementation options were considered:

1. **QML-side derivation**: the dashboard opens its own v1.1
   `torrent.subscribe` stream and computes counts/sums in QML from the
   snapshot/deltas.
2. **Daemon-side aggregation** (chosen): the daemon computes aggregates
   from its committed state and serves them in one small response frame.

Option 1 was rejected because:

- The dashboard would maintain a full mirrored torrent map solely to
  derive eight numbers — a second state engine in QML, which the
  architecture (ADR-0001) and the Phase 0.4 architecture-review checklist
  explicitly forbid ("no duplicate state engine in QML").
- Every dashboard open would ship the full snapshot (~1 003 frames at
  1 000 torrents) for data that aggregates to a few dozen bytes.
- Classification semantics (what counts as "downloading",
  "completed", "active") would live in presentation code, able to drift
  from the daemon's normalized-state contract (ADR-0005).

Option 2 keeps each metric's definition in the daemon — the same layer
that owns state normalization — and makes the dashboard a pure renderer
of one bounded frame. The existing v1.0 `system.status` already
establishes the pattern (cache-served poll frames, never contacting the
backend); v1.3 extends it with the aggregate payload the dashboard
needs. Aggregation cost is one O(N) pass per request over committed
state (µs at N=1 000, measured in benchmarks); no cache or
invalidation machinery is warranted.

## DECISION

Extend IPC v1 to **v1.3** with one request type (`dashboard.status`) and
one response type in two shapes (live and degraded). Rules inherited
from ADR-0004/0005 unchanged: additive only, no existing shape changes,
strict grammar, 4 096-byte frame budget, responses served exclusively
from committed state (answering never contacts qBittorrent).

### Aggregation semantics (single source of truth per field)

All fields derive from the daemon's committed `state.State`
(ADR-0005 normalization). Classifications intentionally reuse the exact
semantics the Phase 0.2/0.3 panel already displays, so panel and
dashboard can never disagree:

- `counts.total` — number of torrents in committed state.
- `counts.active` — torrents with `dlspeed > 0 || upspeed > 0`.
- `counts.downloading` / `seeding` / `paused` — normalized state
  equality (`queued`/`checking`/`error`/`moving`/`other` torrents are
  inside `total` but not broken out in 0.4; counts are NOT expected to
  sum to `total`).
- `counts.completed` — `progress >= 1` (same predicate as the panel's
  Completed filter).
- `aggregate.total_size` — saturating sum of `size`.
- `aggregate.completed_bytes` — saturating sum of `completed`.
- `aggregate.remaining_bytes` — saturating sum of
  `max(0, size - completed)` per torrent (per-torrent clamp before
  summing, so a backend reporting `completed > size` can never produce
  a negative total).
- `active[]` — the "transferring now" list: torrents with
  `dlspeed > 0 || upspeed > 0`, at most 5, ordered by combined current
  speed descending, name ascending for determinism. Current state only —
  explicitly not history.
- `free_space` — qBittorrent `server_state.free_space_on_disk`
  (WebAPI ≥ 2.1.1; live key confirmed on the installed 5.2.3 / 2.15.1
  backend, docs/QBITTORRENT.md). Semantics: free space on the disk of
  qBittorrent's default save path; the value is labeled as such in the
  UI and is absent when the backend has not reported it. The syncer
  commits it with the same last-known-good discipline as the global
  speeds and drops it on degradation (unknown ≠ zero).

Degraded shape: when the backend is unreachable the response carries
`qbittorrent:"unavailable"` plus, iff at least one sync cycle ever
committed, a `last_known` object with counts/aggregate/versions from
the last-known-good state. Speeds and the active list are NOT included
while degraded — the daemon does not know them, and fake zeroes are
forbidden (docs/SECURITY.md truthfulness rules). Before the first
sync completes the degraded shape omits `last_known` entirely.

### Frame budget

Names inside `active[]` are capped (48 runes) and the encoder applies a
halving guard; if pathological names still exceed the frame budget,
trailing `active` entries are dropped — never silently: `counts.active`
always reports the true transferring count, and the list is documented
as "up to 5". Aggregate/counts fields are always present in full.

### Presentation boundary

QML remains presentation-only (ADR-0001): the dashboard renders the
frame, formats bytes/speeds/percentages, and computes nothing beyond
display formatting. History, averages-over-time, and persisted metrics
remain out of scope (Phase 0.8); v1.3 is a current-state surface.

## CONSEQUENCES

- `docs/IPC.md` gains a v1.3 section; contract fixtures cover request
  and both response shapes; anti-drift tests pin exact key sets.
- `unsupported_message` documentation lists `dashboard.status`.
- The daemon gains a `state.Aggregate` pure function (plus tests and
  benchmarks at 10/100/1 000 torrents) and commits
  `free_space_on_disk` into normalized state.
- v1.0–v1.2 clients are unaffected: they never send `dashboard.status`
  and receive no new frames (no broadcast; poll-response only).
