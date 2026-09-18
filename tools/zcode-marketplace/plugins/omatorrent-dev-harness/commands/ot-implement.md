---
description: Implement an already-defined OmaTorrent task — inspect first, smallest coherent change, no opportunistic refactors.
argument-hint: "<task or plan reference>"
---

# /ot-implement

Implement a task that already has a definition or plan. Before writing
anything:

1. Inspect the relevant files and the current Git status; preserve unrelated
   changes.
2. Re-read AGENTS.md and load the relevant docs/ pages (ARCHITECTURE, IPC,
   QBITTORRENT, QUICKSHELL as applicable).
3. Search for existing implementation before writing new code — do not assume
   code does not already exist.
4. Identify the tests affected by the change.

Then implement the smallest coherent change:

- Follow the architectural invariants; if the task as given conflicts with
  one, STOP and report the conflict instead of working around it.
- No opportunistic refactors of unrelated parts; no new dependencies where
  existing project tooling suffices.
- Build/maintain tests with the change when the task includes behavior.
- Run the affected checks; report real output with PASS / FAIL / NOT RUN
  states (use the omatorrent-verification skill).

Do not commit; recommend a commit boundary instead.

Task: $ARGUMENTS
