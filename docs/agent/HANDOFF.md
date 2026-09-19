# OmaTorrent — Session Handoff

This is a checkpoint, not an archive: /ot-handoff replaces it with the
current state. Keep under ~60 lines. Do not paste conversations.

## CURRENT OBJECTIVE

Phase 0.4 MERGED (PR #8 @ merge `df373af`, head `6518550`, issue #7
closed; final record docs/agent/PHASE04.md incl. merge SHA). Next:
Phase 0.5 planning per docs/ROADMAP.md.

## COMPLETED

- Phase 0 (merged, PR #2); Phase 0.2 (merged, PR #4 @ 3e090d5, issue #3
  closed; final record docs/agent/PHASE02.md incl. merge SHA).
- Phase 0.3 MERGED (PR #6 @ `fc8615f`, issue #5 closed; final record
  docs/agent/PHASE03.md incl. merge SHA): ADR-0006 IPC v1.2 staged
  mutation contract, daemon mutation layer, panel actions.
- Phase 0.4: ADR-0007 IPC v1.3 dashboard aggregates (daemon-side
  state.Aggregate, free_space commit), overlay plugin
  local.omatorrent-dashboard (separate plugin — menu model; destroyed
  on close), panel header entry, smoke dashboard stages + regression
  green; live/degraded/recovered/theme screenshots; 50x lifecycle
  torture clean. Record: docs/agent/PHASE04.md.
- Daemon 0.3.0-phase03 deployed via user systemd unit and validated live
  (panel live/degraded/recovered screenshots; journal clean).
- Reviews: architecture/security/QA run post-implementation (verdicts in
  docs/agent/PHASE03.md and the PR).

## UNRESOLVED DECISIONS

- Public plugin namespace (plugins.omarchy.org) — user/marketplace.
- Boot-enable the user service (`systemctl --user enable`) — user.

## BLOCKERS

- None known. Phase 0.4 merged; Phase 0.5 not started.

## TESTS ACTUALLY RUN (Phase 0.3, 2026-09-19)

go build/vet/gofmt/test -race (PASS); harness 82/82; guard 39/39;
plugin validate (PASS); tools/test_quickshell.sh incl. 10-stage v1.2
mutation lifecycle on a disposable magnet (PASS); live research probes
with disposable torrent + no-op hashes (user torrents 3 → 3); secret
scan (clean); shell restart + journal audit (clean). Live
remove-with-files on a torrent WITH real files: NOT RUN (no safe
target); legacy < 2.11.0 backend path: fixture-only.
