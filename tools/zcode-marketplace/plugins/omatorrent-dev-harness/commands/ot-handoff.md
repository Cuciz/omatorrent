---
description: Create or update the concise durable OmaTorrent session handoff (docs/agent/HANDOFF.md).
---

# /ot-handoff

Create or update a durable checkpoint so a fresh session can continue
exactly where this one stopped. Write it to docs/agent/HANDOFF.md, replacing
the previous checkpoint (it is a checkpoint, not an archive).

The handoff must contain exactly:

- CURRENT OBJECTIVE — one sentence.
- COMPLETED — what is done, with evidence pointers (files, commits, test
  results). No narrative.
- UNRESOLVED DECISIONS — open questions and who/what must decide them
  (user, ADR, research).
- BLOCKERS — anything stopping progress, with the concrete unblock step.
- AFFECTED FILES — paths touched this session.
- TESTS ACTUALLY RUN — commands + PASS/FAIL states. Do not list tests that
  were not run.
- NEXT EXACT ACTION — the single next command/task a new session should
  start with.

Do not dump the conversation; do not restate AGENTS.md. Keep it under ~60
lines. If the previous handoff carries still-valid open items, carry them
forward marked as such.

$ARGUMENTS
