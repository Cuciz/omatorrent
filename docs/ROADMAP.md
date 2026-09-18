# OmaTorrent — Roadmap

Status: DRAFT. Version numbers are planning anchors, not promises.
Dependencies and exit criteria use the evidence rules from
`docs/TESTING.md` (omatorrent-verification skill).

## Phases (0.1 → 1.0)

| Ver | Theme | Depends on | Exit criteria (evidence) |
|---|---|---|---|
| 0.1 | Foundations: repo, Go module, daemon skeleton, IPC contract v1, qBittorrent capability probe, bar widget | Phase 0 | handshake + version-reject contract tests pass; live probe against qbittorrent-nox returns version+WebAPI version; bar shows real state or explicit disconnected state |
| 0.2 | Panel + incremental state sync (`sync/maindata` + rid) | 0.1 | maindata incremental updates verified against fixtures; panel lists real torrents; reconnect after daemon restart works |
| 0.3 | Essential torrent actions (pause/resume/add/remove without files) | 0.2 | mutation contract tests; confirmation flow for removal; degraded states truthful |
| 0.4 | Dashboard | 0.2 | dashboard renders health/stats from real daemon data; lifecycle open/close stable |
| 0.5 | Remote qBittorrent + security hardening | 0.3 | TLS + credential handling security-reviewed; remote backend integration test |
| 0.6 | VPN monitoring (defense in depth) | 0.5 | VPN status claims provable; security review of the safety model |
| 0.7 | Storage/NAS safety | 0.4 | storage monitor with real sources; destructive-path protections tested |
| 0.8 | Metrics/history/diagnostics | 0.4 | SQLite schema + migrations tested; data derived from real sources |
| 0.9 | CI, update/rollback, hardening | all | CI green on tagged runs; upgrade + rollback tested; release gates pass |
| 1.0 | Release | 0.9 | full release gate run (omatorrent-release skill) READY verdict |

## Phase 0 — technical foundation planning (not yet started)

Reconnaissance and foundation planning for implementation:

1. Environment prerequisites (Go toolchain is NOT installed on the
   workstation yet — install decision + method, per Arch rules).
2. Read current Omarchy Quattro shell-plugin manual + an installed built-in
   plugin; record the official plugin pattern for the bar/panel/dashboard.
3. qBittorrent capability matrix for the installed 5.2.x and minimum
   supported version (omatorrent-qbt-researcher; docs/QBITTORRENT.md).
4. IPC contract v1 design proposal → ADR review.
5. Product plugin naming/namespace decision.

Exit: a `/ot-plan`-quality plan for 0.1 exists, with acceptance criteria and
verification evidence named.
