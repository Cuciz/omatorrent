---
name: omatorrent-release
description: Use for OmaTorrent release readiness — running the release gates (milestone criteria, verification suite, security review, qBittorrent compatibility, changelog, packaging, rollback, docs freshness, secret scan) and producing the readiness report. Assessment only; never publishes.
---

# OmaTorrent Release Gates

A release is READY only when every gate passes with evidence. This skill
assesses; the user publishes. Automation may tag nothing, push nothing.

## Gates

1. **Milestone criteria** — every exit criterion for the target
   (docs/ROADMAP.md) has recorded evidence, not narrative.
2. **Verification suite** — full run per the omatorrent-verification skill;
   zero unexplained FAIL/NOT RUN on applicable checks.
3. **Security review** — omatorrent-security-reviewer has reviewed all
   high-risk areas changed since the last review; verdicts recorded.
4. **Compatibility** — supported qBittorrent/WebAPI versions in
   docs/QBITTORRENT.md match the adapter's gated behavior; contract tests
   against fixtures for the full claimed matrix are green.
5. **Changelog** — accurate, user-facing, includes upgrade notes and known
   issues; no placeholder entries.
6. **Packaging** — install path validated on a clean profile; upgrade from
   the previous release validated; rollback path (or its absence) documented
   and intentional.
7. **Hygiene** — no secrets or placeholder credentials anywhere in the tree;
   no machine-specific absolute paths; version strings consistent
   (manifest, service, changelog, tags).
8. **Docs** — AGENTS.md, docs/ pages, and ADR states are current; nothing
   contradicts the shipped behavior.

## Procedure

Run the gates in order; record per-gate PASS / FAIL / NOT RUN / BLOCKED with
evidence. Finish with verdict READY / NOT READY, the blocking list, and the
cheapest path to READY. Recommend — never execute — the release steps
(tag, package upload, marketplace submission).
