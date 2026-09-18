# OmaTorrent — Development Environment & Commands

Status: BOOTSTRAP — the repository currently contains the harness and docs
only. This file is the single source for "which commands to run"; update it
when tooling is added.

## Current command set (bootstrap phase)

| Purpose | Command | State |
|---|---|---|
| Harness validation | `python3 tools/validate_harness.py` | AVAILABLE |
| Guard hook behavior test | `bash tools/test_guard_hook.sh` | AVAILABLE |
| Go build/test | — | NOT AVAILABLE (no Go module yet; Go toolchain not installed — see ROADMAP Phase 0) |
| Shell plugin checks | — | NOT AVAILABLE (no product code yet) |

Never claim a NOT AVAILABLE command ran. When Go/QML code lands, their real
commands (go build / go vet / go test ./..., plugin load checks) get added
here with their actual invocation.

## Environment facts (2026-09-18)

- Arch Linux / Omarchy 4.0.4 / Hyprland workstation (see AGENTS.md user
  rules: no sudo without need; prefer official packages).
- python3 3.14, jq 1.8.2, node (via mise) available; **Go NOT installed**.
- qbittorrent-nox 5.2.3 available locally as the integration backend.
- quickshell 0.3.1; omarchy-shell is the running desktop shell.

## Working rules

- Workflow and delegation: see AGENTS.md. Evidence: docs/TESTING.md.
- Durable checkpoints: `/ot-handoff` → docs/agent/HANDOFF.md.
- Git: no commits/pushes unless the user asks; recommend boundaries
  (GitHub conventions below).

## Git and GitHub

- **Official remote**: GitHub is the official remote repository,
  https://github.com/Cuciz/omatorrent (public), configured as `origin`
  (HTTPS via the GitHub CLI credential helper). Local Git remains the
  working history; GitHub mirrors it and hosts collaboration.
- **Issues**: durable bugs and feature requests live in GitHub Issues.
  HANDOFF.md is a session checkpoint, not a bug tracker.
- **Branches and pull requests**: significant development happens on
  feature branches and lands via pull requests; only trivial changes go
  straight to `main`.
- **Releases**: product releases will be published as GitHub Releases
  (see docs/PACKAGING.md).
- **Protected operations**: destructive Git operations (history rewrites,
  force pushes, remote branch deletion) are forbidden unless the user
  explicitly authorizes them beforehand.
