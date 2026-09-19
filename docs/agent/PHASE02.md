# Phase 0.2 — Incremental torrent state and native panel (record)

Tracking: GitHub issue #3; branch `feat/phase02-incremental-panel`.
Status: COMPLETE (pending PR review). Date: 2026-09-18.

## Final merge record (2026-09-19)

- PR #4 **MERGED** into `main` via merge commit
  `3e090d5b79af54925faa0d040c8eeecc6e499f52`
  (normal merge method, matching the PR #2 convention; no squash/rebase).
- Final accepted head: `1da91d350babca7c8a63a4f3971b0158b7478bfb`
  (pre-merge verification: OPEN, base `main` @ `9b0ad63`, MERGEABLE/CLEAN,
  0 review threads, no new commits, no force-push).
- Issue #3 CLOSED (COMPLETED) by the merge.
- Final review status: architecture APPROVE-WITH-NOTES, security
  PASS-WITH-FINDINGS (all findings fixed and verified), QA
  READY-FOR-REVIEW — round-3 re-review confirmed all 8 findings FIXED.
- Final benchmark values: the corrected table below is authoritative
  (1,000-torrent full rebuild 8.09 ms, delta 375 µs, read 347 µs;
  delta cycle is O(N + delta)).

## Delivered

```
qBittorrent sync/maindata?rid=N (read-only, cookie-jar session)
    ↓ internal/qbittorrent adapter (only qBittorrent-aware component)
internal/state.Syncer (rid, full_update rebuilds, partial merges,
    torrents_removed, last-known-good, generation-numbered commits)
    ↓ committed state + change events
IPC v1.1 (ADR-0005: torrent.subscribe → bounded snapshot + deltas)
    ↓
Quickshell panel (read-only) + bar widget (unchanged role, click opens panel)
```

- Phase 0 regression-free: v1.0 contract suite, stale-socket recovery,
  bar proof all re-validated.
- No mutations anywhere (IPC surface read-only by construction).
- torrents/info polling removed from the daemon; system.status derives
  speeds/count from the sync cache.

## Research findings (docs/QBITTORRENT.md sync section)

- rid tracking is SESSION-scoped; the localhost bypass issues a
  `QBT_SID_<port>` cookie that must be persisted (Go cookie jar) for
  deltas to work — live-verified 1 → 2 rid advance with an empty delta.
- Deltas carry PARTIAL torrent objects (only changed fields) — the
  daemon field-merges; full updates carry complete objects.
- server_state present on full updates, ABSENT on no-change deltas
  (defensive merge, last-known-good).
- Any rid mismatch/backend restart → full_update=true → daemon rebuilds
  and diffs against previous committed state (no ghost entries).

## Performance baseline (CORRECTED in review round 2; synthetic fixtures
with verified-unique hashes, i5-1334U; measured with -benchmem)

The first round's 1,000-torrent numbers were invalid (the fixture
generator produced only 256 distinct hashes — review finding 8). Real
measurements:

| Benchmark (measured) | 10 | 100 | 1,000 |
|---|---|---|---|
| Daemon full rebuild cycle | 77 µs | 767 µs | **8.09 ms** (858 KB, 12k allocs) |
| Daemon delta cycle (10 changed) | 54 µs | 72 µs | **375 µs** |
| Daemon state read (map clone) | 2.7 µs | 17 µs | **347 µs** |

Delta-cycle cost, stated precisely (copy-on-write state model): the
cycle clones the previous torrent map before applying a delta
(`cloneTorrents`), so the **algorithmic complexity is O(N + delta)** —
linear in the total torrent count plus the change size — not O(delta).
The **allocation count** on this path is constant (~91, dominated by
decoding the 10 changed items) because the clone is a single map
allocation, but the **bytes allocated scale with N** (~30 KB at 10,
~81 KB at 100, ~303 KB at 1000). The **measured wall-clock** figures
above (~54–375 µs) grow with N accordingly. The measured ~375 µs at
1,000 torrents is acceptable for Phase 0.2; no optimization was made.
State reads clone the committed map per read: O(N) time and bytes, 4-6
allocs.

IPC (measured, TestIPCMetrics): snapshot frames = N+3 (subscribed +
begin + N items + end); largest encoded item frame 283–290 B; a
10-item delta = 1 frame; a 50-item delta + 2 removals = 3 frames. All
frames ≤ 4096 B by construction (verified in tests).

Panel (complexity analysis, not timed): snapshot application is
O(N log N) (map fill + one sort + one view build at snapshot.end);
delta data updates are O(log N) search + O(1) row set; deltas that
change filter membership or rename reposition and rebuild the view
O(N). Panel responsiveness observed live (smooth scroll, instant
filter switch) — no timing measurements taken.

## Evidence matrix (executed 2026-09-18)

| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` / `gofmt -l .` | PASS |
| `go test -race ./...` (all suites) | PASS |
| Harness validation (82/82) + guard hook (39/39) | PASS |
| `omarchy plugin validate plugins/local.omatorrent` | PASS |
| `tools/test_quickshell.sh` (v1.0 discipline + v1.1 subscription) | PASS |
| Daemon under systemd: start, sync, probe | PASS |
| Panel live: real torrents, filters, progress, speeds/ETA/ratio | PASS (screenshots) |
| Panel degraded: daemon stopped → truthful banner + last-known rows retained | PASS (screenshot) |
| Panel recovery: daemon restarted → resubscribe + full snapshot | PASS (screenshot) |
| Bar widget regression: qBT ● rendering, click opens panel | PASS |
| Shell restart → panel reconstructs state | PASS |
| Ghost prevention on rebuild | PASS (TestSyncRestartRebuildAndResync) |
| Secret scan | PASS |
| qBittorrent-restart live resync | NOT RUN live (would require restarting the user's qbittorrent-nox); covered by fixture tests (full_update rebuild) |

## Review verdicts (all three ran after implementation, implementer excluded)

- **Architecture: APPROVE-WITH-NOTES.** All three requirements VERIFIED:
  QML presentation-only (vocabulary audit clean; view sync and filters
  judged presentation per docs/IPC.md client obligations); rid/merge
  logic solely in internal/state (import graph verified); no qBittorrent
  detail on the wire (normalized set only). Fixes applied from notes:
  frame-bound hardening (encode guards), empty arrays never null,
  Subscribe registration made atomic with the snapshot read,
  backendOK dead return removed, subId dead property removed,
  subscribe-after-first-status (one-in-flight discipline), stale
  IPC.md sources corrected, manifest bumped to 0.2.0, contract examples
  + anti-drift test, seq wording amended, second-subscribe behavior
  documented. Accepted/deferred: QML helper dedup into a shared JS
  module, popout-switch niceties, j/k navigation.
- **Security: PASS-WITH-FINDINGS.** Both MEDIUMs fixed: (1) frame-budget
  breach via uncapped backend strings — hashes now validated at
  normalization (40/64 hex, violations discard the cycle preserving
  last-known-good; tests added), categories capped at 128 runes,
  encode-time size guards on snapshot items and pathological delta
  items; (2) O(N²) panel snapshot application — snapshot items now
  batch into source state with a single rebuild at snapshot.end, row
  lookups are O(1) via a maintained hash→row index (viewIndex linear
  scan removed). Also fixed: EncodeDeltas no longer discards marshal
  errors, QML delta path validates items before applying. Explicit
  confirmations from the reviewer: no frame can exceed the budget via
  names (3473 B worst measured), no unbounded memory growth (all
  buffers bounded, drop-close everywhere), no mutation endpoints, no
  secret leakage.
- **QA/release: READY-FOR-REVIEW.** All 10 checks independently
  reproduced PASS (including race suite, benchmarks matching within
  noise, live smoke, daemon lifecycle, secret scan, ghost-prevention
  test). Flag acted on: screenshots re-captured fully framed.

## PR review round 2 (2026-09-19, on PR #4 before merge)

Eight findings addressed (head 0da831f → fixed):

1. **BLOCKER — stale `subId`**: resetSession() assigned a removed
   property (QML runtime error on every disconnect). Removed; component
   audited (zero references). Live proof: shell restart → panel open →
   daemon stop → daemon start → reconnect + resubscribe + recovery with
   ZERO QML errors in the shell journal across the whole cycle.
2. **Snapshot O(N²)**: snapshot items now populate the map only; order
   is built and sorted once at snapshot.end (O(N log N)); single view
   rebuild.
3. **One request in flight across ALL types**: pendingKind state machine
   ("" | status | subscribe); subscribe clears only on the id-verified
   torrent.subscribed response; `subscribed` set on response, not on
   send; timeout guard covers both kinds; reset clears both. Smoke test
   mirrors the same discipline (no concurrent status+subscribe; the old
   intentional violation probe removed).
4. **Rename ordering**: delta renames reposition the hash in the sorted
   order and rebuild the view once. Deterministic model test (A/B
   rename reordering) runs inside the smoke test.
5. **No silent delta drops**: EncodeDeltas returns ok=false when a
   committed item cannot be framed; the server then terminates the
   subscriber (reconnect + fresh snapshot). Snapshot items that cannot
   be framed abort the subscription. Explicit test with a pathological
   control-char item asserts refusal + disconnect.
6. **Snapshot flow control**: the initial snapshot is delivered serially
   with backpressure (bounded 30 s window) — a 1,000-torrent snapshot
   (~1,003 frames) can no longer overflow the 256-frame live queue;
   only post-snapshot deltas use the bounded drop-close queue. Tests:
   n=1/256/1000 complete delivery, slow-snapshot disconnect,
   delta-committed-during-snapshot ordering (strictly after snapshot.end).
7. **Partial server_state merge**: adapter decodes server_state with
   pointer fields (presence-aware at the boundary only); the syncer
   merges only present fields. Tests: full, dl-only, up-only, explicit
   zero, absent, nil — absent preserves last-known.
8. **Invalid 1,000-torrent fixture**: mkHash now generates verified
   unique 40-hex hashes (test checks uniqueness at 10/100/1000/10000);
   ALL benchmarks re-run with real numbers (see the corrected table
   above — materially higher at 1,000; claims updated everywhere).

## Re-review verdicts (round 3, after the eight fixes)

All three reviewers verified all 8 findings FIXED against head e8fec7c:

- **Architecture: APPROVE-WITH-NOTES.** All 8 FIXED with file:line
  evidence; QML snapshot path confirmed O(N log N); no silent-loss
  path; 1,000-torrent snapshot cannot disconnect via queue overflow;
  standing invariants re-checked (QML purity, contract consistency,
  no mutations, stdlib-only).
- **Security: PASS-WITH-FINDINGS** (new items LOW/INFO only, all
  applied post-review: numeric coercion for QML row fields, version-
  string cap at the adapter (64 runes — closes the one >4096-byte
  frame corner, pre-existing from v1.0), writer-fails-fast teardown
  (bounded shutdown latency), removed-hash length guard in
  EncodeDeltas, dead duplicate case label removed). Race suite, frame
  bounds, bounded memory, no-mutation, no-secrets all re-verified by
  execution.
- **QA: READY-FOR-REVIEW.** All 10 checks re-run PASS; the seven named
  fix-round tests confirmed present and passing under -race;
  benchmarks independently reproduced within run-to-run noise
  (full-rebuild +18-22% in the reviewer's run — documented numbers are
  the implementer's measurements, asymptotics identical).

## Known limitations

- Live qBittorrent restart resync not exercised against the user's real
  backend (out of bounds); fixture-proven.
- View rebuild is O(N) when a delta changes filter membership/order;
  in-place updates otherwise.
- Torrent names capped at 512 runes on the wire (panel display only).
- Panel key navigation (j/k) not implemented (first-party pattern
  available; deferred).
