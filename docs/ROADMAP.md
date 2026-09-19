# OmaTorrent — Roadmap

Status: DRAFT. Version numbers are planning anchors, not promises.
Dependencies and exit criteria use the evidence rules from
`docs/TESTING.md` (omatorrent-verification skill).

## Phases (0.1 → 1.0)

| Ver | Theme | Depends on | Exit criteria (evidence) |
|---|---|---|---|
| 0.1 | Foundations: repo, Go module, daemon skeleton, IPC contract v1, qBittorrent capability probe, bar widget | Phase 0 | handshake + version-reject contract tests pass; live probe against qbittorrent-nox returns version+WebAPI version; bar shows real state or explicit disconnected state |
| 0.2 | Panel + incremental state sync (`sync/maindata` + rid) | 0.1 | maindata incremental updates verified against fixtures; panel lists real torrents; reconnect after daemon restart works |
| 0.3 | Essential torrent actions (pause/resume/add/remove, explicit delete-files, staged confirmation) | 0.2 | mutation contract tests; confirmation flow for removal; degraded states truthful |
| 0.4 | Dashboard | 0.2 | dashboard renders health/stats from real daemon data; lifecycle open/close stable |
| 0.5 | Remote qBittorrent + security hardening | 0.3 | TLS + credential handling security-reviewed; remote backend integration test |
| 0.6 | VPN monitoring (defense in depth) | 0.5 | VPN status claims provable; security review of the safety model |
| 0.7 | Storage/NAS safety | 0.4 | storage monitor with real sources; destructive-path protections tested |
| 0.8 | Metrics/history/diagnostics | 0.4 | SQLite schema + migrations tested; data derived from real sources |
| 0.9 | CI, update/rollback, hardening | all | CI green on tagged runs; upgrade + rollback tested; release gates pass |
| 1.0 | Release | 0.9 | full release gate run (omatorrent-release skill) READY verdict |

## Phase 0.4 — Dashboard (MERGED 2026-09-19 via PR #8 @ df373af)

Large-format native Omarchy dashboard from current, truthful daemon
state only (no history/metrics persistence — that is 0.8). Depends on
0.2 state; must not regress 0.3 mutations.

## Phase 0.3 — essential torrent actions (MERGED 2026-09-19 via PR #6 @ fc8615f)

Branch feat/phase03-essential-actions, issue #5 (closed). ADR-0006 IPC
v1.2 staged mutation contract (pause/resume/add/remove with explicit
delete-files), order-independent client, ref replay protection,
disposable-torrent live smoke. See docs/agent/PHASE03.md.

## Phase 0.2 — incremental state + panel (MERGED 2026-09-19 via PR #4 @ 3e090d5)

Branch feat/phase02-incremental-panel, issue #3. sync/maindata rid sync
(session cookie jar), daemon-side normalized state with last-known-good,
IPC v1.1 subscriptions (ADR-0005), native popout panel (read-only),
benchmarks 10/100/1000. See docs/agent/PHASE02.md.

## Phase 0 — technical foundation (DONE 2026-09-18, merged via PR #2)

Foundation proven end-to-end: bar widget → IPC v1 (ADR-0004, NDJSON,
Unix socket) → omatorrent-service (Go) → qBittorrent WebAPI 2.15.1, with
real state only. Delivered: capability matrix (docs/QBITTORRENT.md), IPC
v1 contract + tests, daemon skeleton (config/ipc/qbittorrent/state),
minimal bar proof plugin (local.omatorrent) with truthful degraded
states, systemd user unit, ot-probe debug CLI, isolated Quickshell smoke
test (tools/test_quickshell.sh). Evidence: docs/agent/PHASE0.md.

0.1 exit criteria status: handshake + version-reject contract tests PASS;
live probe PASS (v5.2.3 / 2.15.1); bar shows real state or explicit
disconnected state PASS (observed live).

Remaining from the 0.1 line that Phase 0 deliberately did NOT build
(they belong to 0.1+ polish or 0.2): nothing blocking; CI workflow
(0.9), package/install automation (PACKAGING.md).

## Phase 0.1 — bar widget productization (residual polish line; absorbed incrementally into 0.2+ work)

Tighten the proof into the daily-use widget: speed formatting options,
per-display behavior verification, setting toggles via the native
`settings` schema, `keepLoaded`/lifecycle review, theme-switch
verification, and the 0.2 panel groundwork.
