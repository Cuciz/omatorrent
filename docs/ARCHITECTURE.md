# OmaTorrent — Architecture

Status: DRAFT. Binding decisions live in `docs/adr/`; this file is the map.

## System shape [DECISION]

```text
Omarchy / Quickshell (omarchy-shell)
│
├── BarWidget        (compact transfer state)
├── Panel            (daily-use torrent operations)
└── Dashboard/Overlay(administration, stats, health, safety)
       │
       │ Unix-domain socket, versioned IPC protocol (ADR-0003)
       ▼
omatorrent-service (Go, ADR-0002)
│
├── IPC server
├── state manager
├── qBittorrent adapter (only qBittorrent-aware component)
├── backend abstraction (future non-qBittorrent backends)
├── VPN monitor (defense in depth)
├── storage monitor
├── metrics/history
├── SQLite (state/history only — never secrets)
└── secret provider (only holder of credentials)
```

## Boundaries [DECISION, ADR-0001]

| Boundary | Rule |
|---|---|
| QML → daemon | Presentation only: renders pushed/served state, sends user intents. No business logic, no networking, no secrets. |
| daemon → qBittorrent | Only inside the adapter, behind a narrow interface. |
| IPC | Versioned protocol with handshake; incompatible versions rejected safely. |
| Storage | SQLite for state/history; secrets only in the secret provider. |
| Backend abstraction | Exists so a future backend (e.g. Transmission) can be added; qBittorrent is the only implementation before 1.0. |

## Dependency direction

QML → (nothing; consumes IPC surface) · daemon internals → adapter
interface · everything → small interfaces, wired in main. No package-level
mutable state.

## Confirmed environment facts (2026-09-18, this workstation)

- Omarchy 4.0.4-1 ("Quattro"): the desktop is a single long-lived Quickshell
  process (`quickshell -n -p /usr/share/omarchy/shell`); bar/panels/
  overlays are plugins in it. Restart via `omarchy-restart-shell`.
- quickshell 0.3.1-1 installed.
- qbittorrent-nox 5.2.3-3 installed; WebAPI **2.15.1** probed live
  (docs/QBITTORRENT.md). Local dev backend at 127.0.0.1:8080.
- Go 1.27.1 via mise (repo-scoped; `sudo pacman -S go` recommended
  permanently — docs/DEVELOPMENT.md).
- Installed plugin naming convention observed: `author.plugin-name`
  (e.g. `b.okomart`, `local.networks`). OmaTorrent dev ID:
  `local.omatorrent` (public ID OPEN).

## Phase 0.2 as-built (2026-09-18)

- Daemon state: `internal/state.Syncer` owns the sync/maindata loop —
  rid, full_update rebuilds, partial-field delta merges,
  torrents_removed, session/rid-reset recovery, last-known-good on
  malformed payloads, generation-numbered committed states, and change
  events for subscribers. The adapter (`internal/qbittorrent`) keeps an
  HTTP cookie jar so the bypass-issued session (and with it the rid)
  survives across calls (docs/QBITTORRENT.md). torrents/info polling is
  gone: system.status speeds/count come from the sync cache.
- IPC v1.1 (ADR-0005): `torrent.subscribe` → subscribed + bounded
  snapshot frames (begin/item/end) + `torrent.delta` pushes (seq'd,
  chunked ≤ 4096 B; the initial snapshot is delivered with backpressure
  while live deltas use a bounded 256-frame queue — slow consumers are
  disconnected);
  v1.0 shapes untouched. Server writes are serialized per connection
  through one writer goroutine.
- Panel `plugins/local.omatorrent/Panel.qml`: native popout
  (Ui.Panel + KeyboardPanel + PanelKeyCatcher, first-party clock
  pattern), header (backend state + speeds via system.status poll),
  filters, dense read-only rows with progress tracks; incremental view
  updates (in-place row set; rebuild only on membership/order change).
  Bar widget unchanged in role; click toggles the panel.

## Phase 0 as-built (2026-09-18)

- Daemon `omatorrent-service/` (Go, single module):
  `cmd/omatorrent-service` (wiring, signals, slog/JSON),
  `cmd/ot-probe` (debug IPC client), `internal/ipc` (socket lifecycle +
  strict protocol), `internal/qbittorrent` (only qBittorrent-aware
  component; auth/ban classes, SID re-login), `internal/state`
  (background refresh, backoff 2 s→30 s, snapshot cache),
  `internal/config` (0600-enforced JSON config, defaults).
- IPC v1 per ADR-0004 (NDJSON, hello/health/system.status, no
  mutations). Plugin `plugins/local.omatorrent/` is presentation only
  (Quickshell.Io Socket + SplitParser, 2 s poll, bounded reconnect).
- systemd user unit in `packaging/systemd/`; example frames in
  `contracts/ipc/v1/`.

## Open questions

- [OPEN] Metrics/history retention policy.
- [OPEN] Public plugin namespace (plugins.omarchy.org).
- [OPEN] Push vs versioned-poll state delivery for 0.2 (rid-based
  incremental sync is the qBittorrent-side mechanism either way).
