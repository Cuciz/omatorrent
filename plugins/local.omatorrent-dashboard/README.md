# OmaTorrent Dashboard (local.omatorrent-dashboard)

Companion overlay plugin for [local.omatorrent](../local.omatorrent/):
a large, native Omarchy dashboard card showing CURRENT state only —
live transfer speeds, torrent population counts, aggregate data, and
the transferring-now list — from the omatorrent-service daemon
(IPC v1.3 `dashboard.status`, ADR-0007).

- No history, no persisted metrics (Phase 0.8 scope), no torrent-list
  mirror in QML: the daemon computes every displayed number.
- Truthful degraded states: daemon offline, qBittorrent unreachable
  with clearly-labeled last-known data, first-sync pending.
- Lifecycle: summoned via `omarchy-shell shell toggle
  local.omatorrent-dashboard` or the torrent panel's header button
  (▤). Not keepLoaded — the overlay is destroyed on close and costs
  nothing while hidden. Escape or click-outside closes.
