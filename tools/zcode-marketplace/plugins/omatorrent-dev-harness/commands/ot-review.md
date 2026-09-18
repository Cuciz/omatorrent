---
description: Independent evidence-based review of the actual OmaTorrent diff — architecture, regressions, unsafe assumptions, tests, complexity, security, stale docs.
argument-hint: "[<commit-range or files>] (default: working tree)"
---

# /ot-review

Review the actual diff — never a description of it. Use independent read-only
reviewers (the omatorrent-architect agent); the reviewer must
not be the agent that implemented the change.

1. Establish the diff under review: `git diff` for the working tree, or the
   given commit range. If there is nothing to review, say so and stop.
2. Delegate the review to the appropriate harness agents in parallel where
   independent: omatorrent-architect (boundaries, drift, complexity),
   omatorrent-security-reviewer (secrets, socket, deletion, injection — for
   any change touching risky areas), and for QML changes the Quickshell
   rules from the omatorrent-quickshell-development skill.
3. Each reviewer works from the real diff with file:line evidence.

Look specifically for: architectural invariant violations; regression risk;
unsafe assumptions (unverified API contracts, missing error paths); missing
tests; unnecessary complexity; security problems; documentation that became
stale because of the change.

Produce findings ordered by severity, each with evidence and a concrete
alternative, and an overall verdict: ACCEPT / REWORK (list the blocking
findings) / NEEDS DISCUSSION (requires a user or ADR decision).

Review target: $ARGUMENTS
