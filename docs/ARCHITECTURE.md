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

- Omarchy 4.0.4 ("Quattro"): the desktop is a single long-lived Quickshell
  process (`omarchy-shell`); bar/panels/overlays are plugins in it.
- quickshell 0.3.1 installed.
- qbittorrent-nox 5.2.3 installed (local backend for development/testing).
- Installed plugin naming convention observed: `author.plugin-name`
  (e.g. `b.okomart`, `local.networks`).

## Open questions

- [OPEN] IPC message encoding (JSON lines vs binary) — decided at Phase 1
  contract design.
- [OPEN] Metrics/history retention policy.
- [OPEN] systemd user service unit name and socket path (XDG_RUNTIME_DIR).
