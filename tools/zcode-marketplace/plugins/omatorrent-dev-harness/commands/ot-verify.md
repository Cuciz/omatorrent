---
description: Run all verification relevant to the changed OmaTorrent components, reporting PASS / FAIL / NOT RUN / NOT AVAILABLE per check.
argument-hint: "[<scope>] (default: components changed since last verify)"
---

# /ot-verify

Run every verification relevant to the changed components. Follow the
omatorrent-verification skill: a change is not complete until the relevant
checks have actually run.

1. Determine the changed scope (git status/diff, or the given argument).
2. Choose the checks that apply to that scope from docs/DEVELOPMENT.md and
   docs/TESTING.md (build, vet, unit/contract/integration tests, packaging
   dry-run, manual UI observation where applicable).
3. Run each check for real. Never report a test as passing unless it ran
   successfully in this session; never silently skip a check.
4. Report a table with, per check: name, command, state — PASS / FAIL /
   NOT RUN (say why) / NOT AVAILABLE (tooling does not exist yet) — plus the
   key evidence line for PASS/FAIL.
5. For FAIL: the minimal repro and the suspected cause. For NOT AVAILABLE:
   what tooling gap to close.

Finish with the honest overall state: complete only if every applicable
check is PASS, and name anything left unproven.

Verify target: $ARGUMENTS
