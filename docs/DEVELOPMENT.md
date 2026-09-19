# Sprout — Development Environment & Commands

Status: PHASE 0 IMPLEMENTED. This file is the single source for "which
commands to run"; update it when tooling changes.

## Environment facts (verified 2026-09-18, this workstation)

- Arch Linux / Omarchy 4.0.4-1 ("Quattro") / Hyprland; shell process
  `quickshell -n -p /usr/share/omarchy/shell` (quickshell 0.3.1-1).
- qbittorrent-nox 5.2.3-3, WebUI 127.0.0.1:8080, WebAPI **2.15.1**,
  localhost auth bypass on (dev backend; docs/QBITTORRENT.md).
- gnome-keyring + libsecret (secret-tool) present — a hard `omarchy`
  package dependency; omatorrent-service stores remote credentials
  there (ADR-0009). Missing/locked keyring ⇒ the daemon degrades to
  `secrets_unavailable` (fail closed, no plaintext fallback).
- Go 1.27.1 via mise, **repo-scoped** (`.mise.toml`) — installed without
  sudo because interactive sudo was unavailable. The recommended permanent
  method remains `sudo pacman -S go` (official `extra/go`). Re-run
  `mise install` in the repo if the toolchain is missing.
- python3, jq, grim+magick (screenshots) available.

## Command set

| Purpose | Command | State |
|---|---|---|
| Harness validation | `python3 tools/validate_harness.py` | AVAILABLE |
| Guard hook test | `bash tools/test_guard_hook.sh` | AVAILABLE |
| Brand asset validation (0.5.1) | `python3 tools/validate_brand.py` | AVAILABLE |
| Brand asset regeneration | `python3 assets/brand/generate.py` | AVAILABLE |
| Go build | `cd omatorrent-service && go build ./...` | AVAILABLE |
| Go vet / format | `go vet ./... && gofmt -l .` | AVAILABLE |
| Go unit + contract tests | `go test -race ./...` | AVAILABLE |
| Install daemon binary | `go build -o ~/.local/bin/omatorrent-service ./cmd/omatorrent-service` | AVAILABLE |
| IPC probe (debug CLI) | `go run ./cmd/ot-probe [-count N] [-interval 2s]` | AVAILABLE |
| Isolated Quickshell↔daemon smoke (v1.0 + v1.1 subscription) | `bash tools/test_quickshell.sh` | AVAILABLE |
| Benchmarks (10/100/1000 torrents) | `cd omatorrent-service && go test -bench . -benchmem -run XXX ./internal/state/` | AVAILABLE |
| Remote-path benchmarks (auth cycle HTTP/TLS, connection.test) | `cd omatorrent-service && go test -bench 'AuthCycle\|ConnectionTestPath' -run XXX ./internal/qbittorrent/ ./internal/connection/` | AVAILABLE |
| Plugin manifest validation | `omarchy plugin validate plugins/local.omatorrent` | AVAILABLE |
| Install plugin (dev) | copy `plugins/local.omatorrent/` → `~/.config/omarchy/plugins/` then `omarchy plugin enable local.omatorrent` | AVAILABLE |
| Service control | `systemctl --user {start,stop,restart,status} omatorrent-service` | AVAILABLE |
| Service logs | `journalctl --user -u omatorrent-service` | AVAILABLE |
| Shell restart (purge plugin instances) | `omarchy-restart-shell` | AVAILABLE |

Config: `$XDG_CONFIG_HOME/omatorrent/service.json` (optional; JSON, must
be 0600; missing file → defaults pointing at the local dev backend; a
non-empty `qbittorrent.password` fails load with a migration error —
credentials live in the Secret Service since 0.5).
Connection profile: `$XDG_CONFIG_HOME/omatorrent/connection.json`
(daemon-written via IPC `connection.configure`, 0600, strict schema;
absent → the service.json endpoint applies).
Socket: `$XDG_RUNTIME_DIR/omatorrent/service.sock`.
Daemon flags: `-config`, `-socket`, `-connection` (or
`$OMATORRENT_CONNECTION`).

## Known workflow quirks (observed 2026-09-18)

- Quickshell `Socket.write` does NOT append a line terminator — every IPC
  frame must be written as `JSON.stringify(msg) + "\n"`.
- Setting `connected: true` at QML construction time does not fire
  `connectionStateChanged`; the initial hello must be bootstrap-aware
  (see plugins/local.omatorrent/BarWidget.qml `ensureSession()`).
- Editing a plugin file in `~/.config/omarchy/plugins/` triggers a shell
  rescan, but an already-instantiated bar widget may keep running old
  code; use `omarchy-restart-shell` after widget edits for a clean state.
- An unclean daemon kill (SIGKILL) leaves a stale socket; the next start
  recovers it safely (proven-dead probe + identity re-check, ADR-0004
  amendment 2). A live daemon on the socket or any ambiguous state still
  fails closed — check `journalctl --user -u omatorrent-service`.

## Working rules

- Workflow and delegation: see AGENTS.md. Evidence: docs/TESTING.md.
- Durable checkpoints: `/ot-handoff` → docs/agent/HANDOFF.md.
- Git: no commits/pushes unless the user asks; recommend boundaries
  (GitHub conventions below).

## Git and GitHub

- **Official remote**: https://github.com/Cuciz/omatorrent (public),
  configured as `origin` (HTTPS via the GitHub CLI credential helper).
- **Issues**: durable tracking lives in GitHub Issues (Phase 0: #1).
  HANDOFF.md is a session checkpoint, not a bug tracker.
- **Branches and pull requests**: significant development happens on
  feature branches and lands via pull requests; only trivial changes go
  straight to `main`.
- **Releases**: product releases will be published as GitHub Releases
  (see docs/PACKAGING.md).
- **Protected operations**: destructive Git operations (history rewrites,
  force pushes, remote branch deletion) are forbidden unless the user
  explicitly authorizes them beforehand.
