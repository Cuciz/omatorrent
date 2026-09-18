# omatorrent-dev-harness

Development harness for the OmaTorrent project. This plugin is **development
infrastructure** — it is NOT the future Omarchy product plugin named
OmaTorrent and contains no product code.

## Contents

- **Agents** (6): omatorrent-architect (read-only architecture guardian),
  omatorrent-quickshell (QML implementation), omatorrent-backend (Go daemon
  implementation), omatorrent-qbt-researcher (read-only API research),
  omatorrent-security-reviewer (read-only adversarial review),
  omatorrent-qa-release (tests/CI/release).
- **Skills** (6): omatorrent-architecture, omatorrent-quickshell-development,
  omatorrent-qbittorrent-api, omatorrent-security-review,
  omatorrent-verification, omatorrent-release.
- **Commands** (7): /ot-status, /ot-plan, /ot-implement, /ot-review,
  /ot-verify, /ot-handoff, /ot-release.
- **Hooks** (2): a SessionStart one-line reminder, and a PreToolUse guard
  that denies obviously destructive Bash commands.

## Safety hook behavior

The guard denies Bash commands that would destroy work or systems: recursive
forced deletion outside the workspace or of `.git`, filesystem device
writers, `git reset --hard`, `git clean -f/--force`, force/delete push
(bare `--force`, `-f`, `--delete`, `-d`, `+ref:ref`, `:ref`), `git tag
--delete`, `git branch -D`, discard-all checkout/restore, and recursive
chmod/chown on protected system trees. `--force-with-lease` and
`--force-if-includes` pushes and `git restore --staged .` are allowed. It
fails open: any internal error allows the command, so a broken hook never
breaks a session.

Deliberate human override (only with user approval): prefix the command with
`OT_ALLOW_DESTRUCTIVE=1` or append the comment `# ot-allow-destructive`.

This protects the development workflow only. It is NOT product security.

## Install (manual, via the ZCode UI)

1. Settings → Plugin Management → Discover tab → **+** (Add marketplace).
2. Choose this marketplace directory:
   `<repo>/tools/zcode-marketplace` (contains `marketplace.json`).
3. Install **omatorrent-dev-harness** from the marketplace listing.
4. New plugins are enabled by default; verify agents/skills/commands appear
   (Settings → Subagents / Skills, and the `/` menu).
5. **Start a NEW ZCode session** — agents, skills, commands, and especially
   hooks only join new sessions; they do not hot-reload.

Requirements: `python3` on PATH (hook scripts). See docs/HARNESS.md for the
full manual, including verification and troubleshooting pointers.
