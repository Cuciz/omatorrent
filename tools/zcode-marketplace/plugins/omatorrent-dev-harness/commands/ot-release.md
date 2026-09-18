---
description: OmaTorrent release-readiness assessment — runs the release gates and reports; never publishes anything.
argument-hint: "<target version/milestone>"
---

# /ot-release

Execute a full release-readiness assessment for the given target. Follow the
omatorrent-release skill. You assess and report — you never publish, tag,
push, or make the release decision; the user decides.

1. Scope: which milestone/version, from docs/ROADMAP.md.
2. Run each release gate and record its state with evidence:
   - Acceptance criteria for the milestone all evidenced.
   - Full verification suite green (per /ot-verify semantics).
   - Security review completed on high-risk areas
     (omatorrent-security-reviewer) since the last review.
   - Compatibility: qBittorrent/WebAPI version support matches
     docs/QBITTORRENT.md claims; contract tests green.
   - Changelog accurate and user-facing notes complete.
   - Packaging validated (install/upgrade path; rollback path where
     promised).
   - No secrets/placeholder credentials anywhere in the tree.
   - Documentation current (AGENTS.md, docs/, ADR states accurate).
3. Report per-gate: PASS / FAIL / NOT RUN / BLOCKED + evidence.
4. Finish with: release verdict (READY / NOT READY), the blocking items,
   and the cheapest path to READY.

Target: $ARGUMENTS
