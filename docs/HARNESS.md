# Sprout — ZCode Development Harness

The harness is the `omatorrent-dev-harness` ZCode plugin in
`tools/zcode-marketplace/`. It is development infrastructure, never the
product plugin.

## Components

- **Agents (6)** in `agents/`: architect (read-only), quickshell, backend,
  qbt-researcher (read-only), security-reviewer (read-only), qa-release.
  All use `model: inherit`, bounded `maxTurns`, tool lists matched to role.
- **Skills (6)** in `skills/`: architecture, quickshell-development,
  qbittorrent-api, security-review, verification, release.
- **Commands (7)** in `commands/`: ot-status, ot-plan, ot-implement,
  ot-review, ot-verify, ot-handoff, ot-release.
- **Hooks (2)** in `hooks/`: SessionStart one-line reminder; PreToolUse
  Bash guard against destructive commands (see below).

## Installation (manual — the ZCode plugin UI is not automatable from here)

1. ZCode → Settings → Plugin Management → **Discover** tab → **+** button.
2. Add marketplace → choose local directory → select
   `<repo>/tools/zcode-marketplace` (the folder containing
   `marketplace.json`).
3. Install `omatorrent-dev-harness`; it is enabled by default.
4. Verify: Settings → Subagents (6 omatorrent agents), Settings → Skills
   (6 omatorrent skills), `/` menu shows the 7 `/ot-*` commands, plugin
   detail view shows both hooks.
5. **Start a NEW session** — plugin agents, commands, and hooks only join
   new sessions; they do not hot-reload.

Requires `python3` on PATH (hooks). Validated formats against ZCode docs
(plugins, subagents, skills, commands, hooks) and the installed official
plugins on this machine.

## Guard hook contract

- Event: PreToolUse, matcher `Bash`. Denies: recursive forced deletion
  outside the workspace or of `.git`; filesystem device writers
  (mkfs/wipefs/blkdiscard/shred/dd-to-device); `git reset --hard`,
  `git clean -f/--force`, discard-all checkout/restore (with or without
  `--`, including `--worktree`), push with bare `--force` / `-f` /
  `--delete` / `-d` / `+ref:ref` / `:ref` refspecs, `git tag --delete`,
  `git branch -D`; recursive chmod/chown on protected system trees.
  `--force-with-lease` and `--force-if-includes` pushes and
  `git restore --staged .` are allowed. When shell tokenization fails
  (e.g. unterminated quotes), a cruder recursive-rm fallback still applies.
- Override (human-approved only): `OT_ALLOW_DESTRUCTIVE=1` env prefix or
  `# ot-allow-destructive` comment.
- Fails open on internal errors; development-workflow safety only.

## Durable state policy

ZCode Goal for current work; `docs/agent/HANDOFF.md` for checkpoints
(`/ot-handoff`); Git as history. No duplicate scheduler, no extra
machine-readable state files.

## Validation & troubleshooting

Run `python3 tools/validate_harness.py` after any harness change (checks
JSON, frontmatter, paths, names, secrets). If something does not load after
install: confirm a NEW session was started; then use the zcode-guide
plugin's diagnosing-plugins / diagnosing-skills / diagnosing-commands /
diagnosing-hooks guides. Known doc discrepancy (recorded 2026-09-18): the
plugin docs page lists an `npm` marketplace source; the installed client's
guide says npm/pip sources are unsupported — local-directory sources work
either way (what we use).

## Non-goals

No MCP servers (no concrete missing capability; revisit with justification).
No product code inside the harness. No policy engine — two hooks only.
