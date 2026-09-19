# local.omatorrent — Sprout bar widget + torrent panel

Development-namespace plugin (`local.` prefix; public marketplace ID is
still OPEN — see ADR-0004) carrying the Sprout product identity
(Phase 0.5.1, docs/BRAND.md): compact glyph in the bar, branded panel
header and empty state. Technical IDs keep the `omatorrent` name
deliberately.

## What it does

An Omarchy bar widget showing real qBittorrent transfer state:

- `[glyph] ↓ … ↑ …` — live transfer speeds
- `[glyph] qBT ●` — connected to qBittorrent, idle
- `[glyph] qBT ERROR` — daemon reachable, qBittorrent unreachable
- `[glyph] qBT OFFLINE` — omatorrent-service not running

Tooltip shows the qBittorrent app/WebAPI versions and torrent count.
The panel (bar-widget popout) lists torrents with progress, filters,
pause/resume, magnet add, safe removal, and the qBittorrent connection
settings (Phase 0.5).

## Architecture (binding)

Presentation only (ADR-0001): the widget speaks IPC v1 (ADR-0004, LF
framed JSON) to `omatorrent-service` over
`$XDG_RUNTIME_DIR/omatorrent/service.sock` using Quickshell.Io
`Socket` + `SplitParser`. It performs **no** qBittorrent HTTP calls,
holds no secrets, and makes no business decisions. Poll cadence 2 s;
reconnect with bounded backoff (1 s → 10 s).

The daemon is a systemd user service (`omatorrent-service`), independent
of the shell process. See the repository docs (`docs/IPC.md`,
`docs/ARCHITECTURE.md`) for the contracts.

## Install (development)

```sh
omarchy plugin validate plugins/local.omatorrent
omarchy plugin add plugins/local.omatorrent   # or copy to ~/.config/omarchy/plugins/
omarchy plugin enable local.omatorrent
```
