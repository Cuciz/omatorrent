# Phase 0.2 — Incremental torrent state and native panel (record)

Tracking: GitHub issue #3; branch `feat/phase02-incremental-panel`.
Status: COMPLETE (pending PR review). Date: 2026-09-18.

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

## Performance baseline (synthetic fixtures; i5-1334U)

| Benchmark | 10 | 100 | 1,000 |
|---|---|---|---|
| Full rebuild cycle | 71 µs | 736 µs | **2.02 ms** |
| Delta cycle (10 changed) | 40 µs | 65 µs | **117 µs** |
| State read (snapshot clone) | 2.8 µs | 21 µs | **75 µs** |

IPC payload: snapshot item ≈ 200–260 B (bounded frames); a 1,000-item
snapshot = ~1,003 bounded frames once per subscription, then deltas of
only changed items. No O(N) transfer per poll cycle; the panel applies
deltas with in-place row updates (view rebuild only on membership/order
change). Panel responsiveness observed live (smooth scroll, instant
filter switch).

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

## Review verdicts

- Architecture review: PENDING (filled in the PR).
- Security review: PENDING (filled in the PR).
- QA/release review: PENDING (filled in the PR).

## Known limitations

- Live qBittorrent restart resync not exercised against the user's real
  backend (out of bounds); fixture-proven.
- View rebuild is O(N) when a delta changes filter membership/order;
  in-place updates otherwise.
- Torrent names capped at 512 runes on the wire (panel display only).
- Panel key navigation (j/k) not implemented (first-party pattern
  available; deferred).
