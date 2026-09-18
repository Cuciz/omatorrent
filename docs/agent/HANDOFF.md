# OmaTorrent — Session Handoff

This is a checkpoint, not an archive: /ot-handoff replaces it with the
current state. Keep under ~60 lines. Do not paste conversations.

## CURRENT OBJECTIVE

Phase 0 end-to-end proof complete; PR ready for review.

## COMPLETED

- Branch `feat/phase0-foundation` (from main). GitHub issue #1.
- Daemon: `omatorrent-service/` (ipc/qbittorrent/state/config, tests,
  ot-probe). IPC v1 contract (ADR-0004 + docs/IPC.md).
- Plugin: `plugins/local.omatorrent/` bar proof; installed + enabled in
  `~/.config/omarchy/plugins/`, live in the bar.
- systemd user service installed at
  `~/.config/systemd/user/omatorrent-service.service`, running
  (`systemctl --user start`, NOT boot-enabled — user decision).
- Evidence: docs/agent/PHASE0.md; screenshots docs/screenshots/.

## UNRESOLVED DECISIONS

- Public plugin namespace (plugins.omarchy.org) — user/marketplace.
- Boot-enable the user service (`systemctl --user enable`) — user.
- Permanent Go install via `sudo pacman -S go` (agent had no sudo) — user.

## BLOCKERS

- None for Phase 0. Reviews: verdicts to be recorded in the PR.

## AFFECTED FILES

Everything under `feat/phase0-foundation` vs main (see PR diff).

## TESTS ACTUALLY RUN

go build/vet/test -race (PASS); tools/validate_harness.py (PASS 82/82);
tools/test_guard_hook.sh (PASS 39/39); omarchy plugin validate (PASS);
tools/test_quickshell.sh (PASS); live ot-probe + bar observation (PASS).
Full matrix: docs/agent/PHASE0.md.

## NEXT EXACT ACTION

Review/merge the Phase 0 PR; then start 0.1/0.2 planning (panel +
incremental sync via `sync/maindata` rid).
