# local.omatorrent — OmaTorrent Phase 0 bar proof

Development-namespace plugin (`local.` prefix; public marketplace ID is
still OPEN — see ADR-0004). This is the Phase 0 architecture proof, not
the product UI.

## What it does

A minimal Omarchy bar widget showing real qBittorrent transfer state:

- `↓ … ↑ …` — live transfer speeds
- `qBT ●` — connected to qBittorrent, idle
- `qBT ERROR` — daemon reachable, qBittorrent unreachable
- `qBT OFFLINE` — omatorrent-service not running

Tooltip shows the qBittorrent app/WebAPI versions and torrent count.

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
