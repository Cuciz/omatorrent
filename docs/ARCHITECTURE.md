# Sprout — Architecture

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
├── connection manager (remote profiles, TLS policy, backend epochs)
├── secret provider (only holder of credentials — ADR-0009)
├── metrics/history
└── SQLite (state/history only — never secrets)
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
  (e.g. `b.okomart`, `local.networks`). Sprout dev ID (omatorrent retained as the technical id, docs/BRAND.md):
  `local.omatorrent` (public ID OPEN).

## Phase 0.4 as-built (2026-09-19)

- IPC v1.3 (ADR-0007): `dashboard.status` request/response (live +
  degraded shapes). Every dashboard number is computed daemon-side by
  `state.Aggregate` over the committed state (O(N + A log A) — one pass
  over N torrents plus a sort of the A active candidates, worst case
  O(N log N); saturating sums,
  classification semantics identical to the panel filters) and served
  from cache — answering never contacts qBittorrent. `free_space_on_disk`
  is now committed into normalized state (last-known-good, dropped on
  degradation — unknown ≠ zero).
- Dashboard `plugins/local.omatorrent-dashboard/`: a separate
  overlay-kind plugin (the omarchy.menu model — bar-widget + companion
  overlay plugins), NOT a kind on local.omatorrent: adding "overlay" to
  its kinds would flip `omarchy-shell shell toggle local.omatorrent`
  routing from the bar-widget panel to the shell's panel loader and
  break summon() of the panel. Summonable via the shell CLI and the
  panel header button (first-party `bar.run("omarchy-shell shell
  toggle …")`); the footer opens the panel through the documented host
  CLI (`Util.execDetached("omarchy-shell shell summon
  local.omatorrent")`) — the capability-scoped shell API a plugin
  receives cannot summon OTHER plugins (PluginShellApi gate, review
  finding). Not keepLoaded:
  the shell's Loader destroys the overlay (and its IPC connection) on
  close — a hidden dashboard holds no sockets and runs no timers.
  Window skeleton is the first-party overlay pattern (emojis/clipboard):
  full-anchor layer-shell PanelWindow (Overlay layer, Exclusive keyboard
  focus while open), scrim, centered BorderSurface card with
  Color.menu/Style tokens; Escape/click-outside close via dismiss()
  (close() is host-invoked only — calling shell.hide from close()
  recurses; first-party close/dismiss split).

## Phase 0.5 as-built (2026-09-19)

- Connection management (ADR-0008): `internal/connection` owns the
  profile (daemon-written `connection.json`, strict schema, atomic
  0600 writes), strict URL validation (http/https, no userinfo/query/
  fragment, reverse-proxy path prefix), the HTTP/HTTPS policy
  (loopback exempt; non-loopback HTTP requires the persisted
  acknowledgement) and the fail-closed TLS modes (system roots /
  explicit CA bundle / certificate pinning — never a disabled
  verification). One-shot `connection.test` probes use temporary
  clients (no state change); `connection.configure` validates →
  applies the explicit secret intent → writes → switches the backend
  EPOCH: syncer discards stale-epoch commits and publishes the old
  torrents as removals, the mutator refuses in-flight switches and
  settles orphaned pendings as ambiguous — a mutation can never
  retarget across backends. Auth frugality: auth-class failures back
  off 30 s→10 min and go sticky after 3 consecutive bad-credential
  logins (below qBittorrent's 5-attempt IP ban).
- Secrets (ADR-0009): `internal/secrets` talks to the Secret Service
  via `secret-tool` (secret through stdin/stdout pipes only, ASCII
  label — GLib charset lesson recorded in PHASE05.md); no plaintext
  fallback anywhere; `service.json` passwords fail load with a
  migration error.
- Adapter: version-adaptive login (5.2: 204/401; ≤ 5.1: Ok./Fails.),
  no Origin/Referer, refused redirects everywhere, 202-Accepted adds,
  fetch-per-login passwords (zeroed), logout on switch.
- IPC v1.4 (additive): `connection.status/test/configure`; password is
  transit-only and wiped after the handler; responses carry host
  labels, versions, fingerprints and fixed codes only. v1.0–v1.3
  shapes byte-compatible.
- UI: settings is a MODE of the torrent panel (native Omarchy pattern;
  gear glyph; password field write-only) reachable from the dashboard
  footer via the host CLI; panel/dashboard show differentiated
  connection classes (auth_required/auth_failed/banned/tls_*/
  secrets_unavailable/invalid_configuration), a display-safe remote
  host label, insecure-HTTP badges, and auth-lost callouts. The bar
  widget is unchanged.

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
  component; auth/ban classes, SID re-login; mutation endpoints with
  version-gated stop/start vs legacy pause/resume), `internal/state`
  (background refresh, backoff 2 s→30 s, snapshot cache),
  `internal/mutate` (Phase 0.3 mutation orchestration per ADR-0006:
  validation against committed state, bounded submission, ref replay
  ring, state-derived confirmation), `internal/config` (0600-enforced
  JSON config, defaults).
- IPC v1 per ADR-0004 + v1.1 read-only state (ADR-0005) + v1.2 staged
  mutations (ADR-0006: torrent.pause/resume/add/remove →
  mutation.accepted/rejected → state-derived mutation.result
  confirmed|timeout; accepted ≠ confirmed). Plugin
  `plugins/local.omatorrent/` is presentation only (Quickshell.Io
  Socket + SplitParser, 2 s poll, bounded reconnect; intents,
  confirmations and result rendering — no qBittorrent vocabulary).
- systemd user unit in `packaging/systemd/`; example frames in
  `contracts/ipc/v1/`.

## Open questions

- [OPEN] Metrics/history retention policy.
- [OPEN] Public plugin namespace (plugins.omarchy.org).
- [OPEN] Push vs versioned-poll state delivery for 0.2 (rid-based
  incremental sync is the qBittorrent-side mechanism either way).
