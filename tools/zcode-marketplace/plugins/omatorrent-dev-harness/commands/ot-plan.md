---
description: Research-first bounded implementation plan for an OmaTorrent task. Produces the plan only — no implementation.
argument-hint: "<objective>"
---

# /ot-plan

Research first, then produce a bounded implementation plan. Do not implement
anything, do not create product files.

1. Research: read AGENTS.md, the relevant docs/ pages and ADRs, and inspect
   the existing code for what already exists. Use the omatorrent-qbt-researcher
   agent when qBittorrent API facts are needed, and parallel read-only agents
   where useful.
2. Produce a plan with exactly these sections:
   - OBJECTIVE — one sentence.
   - SCOPE — the concrete changes; list impacted modules.
   - NON-GOALS — what this deliberately does not touch.
   - AFFECTED CONTRACTS — IPC, adapter interface, storage schema, plugin
     boundaries; note if an ADR is required first (omatorrent-architecture
     skill).
   - OWNING AGENT — the single agent that will implement (from the harness).
   - DEPENDENCIES — on other tasks, decisions, or environment prerequisites.
   - RISKS — ranked, each with its mitigation.
   - ACCEPTANCE CRITERIA — observable `done_when` conditions.
   - VERIFICATION EVIDENCE — the exact commands/observations that will count
     as proof, per criterion.
3. If research shows the task violates an architectural invariant or needs a
   decision not yet recorded as an ADR, stop and state that instead.

Target: $ARGUMENTS
