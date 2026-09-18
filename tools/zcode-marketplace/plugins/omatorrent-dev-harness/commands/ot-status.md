---
description: Read-only OmaTorrent project status — branch, dirty files, milestone, architecture changes, known failures, blockers, next action.
---

# /ot-status

Produce a read-only status snapshot of the OmaTorrent repository. Do not
modify anything.

1. Git: current branch, dirty/untracked files (`git status --short`,
   `git log --oneline -5`). If Git is not initialized, say so and stop.
2. Milestone: which roadmap milestone (docs/ROADMAP.md) is in progress, per
   its exit criteria.
3. Architecture: any ADRs added or changed since the last status
   (docs/adr/); any known architectural drift.
4. Tests: run the fastest project checks only if a test setup exists
   (docs/DEVELOPMENT.md); otherwise state NOT AVAILABLE. Never invent results.
5. Handoff: summarize docs/agent/HANDOFF.md if it exists — current objective,
   unresolved decisions, blockers.
6. Finish with: NEXT RECOMMENDED ACTION — one concrete, small step.

Report every item with an explicit state: OK / STALE / MISSING / NOT RUN.
Target: $ARGUMENTS
