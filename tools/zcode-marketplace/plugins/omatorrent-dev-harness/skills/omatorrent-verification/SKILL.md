---
name: omatorrent-verification
description: Use for the standard evidence-driven OmaTorrent verification workflow — running the checks that apply to a change and reporting honest states. A change is not complete until the relevant checks have actually run. Covers build, unit/contract/integration tests, runtime validation, and the PASS / FAIL / NOT RUN / NOT AVAILABLE / BLOCKED reporting vocabulary.
---

# OmaTorrent Verification Workflow

Evidence-driven completion: no check, no claim. Every task's acceptance
criteria name their evidence (`done_when`); this skill is how you collect it.

## States — use exactly these

- **PASS** — the check ran in this session and succeeded; cite the evidence
  line.
- **FAIL** — ran and failed; include the failing output and minimal repro.
- **NOT RUN** — applicable but not executed; state why and when it will run.
- **NOT AVAILABLE** — the tooling/check does not exist yet; name the gap.
- **BLOCKED** — an external prerequisite prevents running it; name it.

Never fabricate output, never silently skip a check, never let narrative
reassurance replace a failed check.

## Workflow

1. Determine scope: which components changed (git diff/status).
2. Select applicable checks from docs/DEVELOPMENT.md and docs/TESTING.md:
   build, vet/lint, unit tests, contract tests (IPC, adapter fixtures),
   integration tests, packaging dry-run, and manual runtime observation for
   UI/service behavior.
3. Run them for real, cheapest first, capturing actual output.
4. Report a per-check table (name, command, state, evidence) and an honest
   overall verdict: COMPLETE only when every applicable check is PASS and
   nothing applicable was skipped.

## Runtime validation requirements

- Services: real process/unit state and logs, not just successful build.
- Shell UI: observed behavior in the running shell (load, open/close,
  reconnect, theme), not just QML that parses.
- Configuration: the consuming software actually loads it.
- Degraded states: verify the daemon-unreachable / qBittorrent-unreachable
  paths explicitly — they are product behavior, not edge cases.
