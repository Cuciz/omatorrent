# OmaTorrent — Quickshell / Omarchy Quattro Development Notes

Status: STARTED — verified environment facts plus the rules from the
omatorrent-quickshell-development skill. Version-sensitive; re-verify per
installed Omarchy version.

## Confirmed environment facts (2026-09-18, this workstation)

- Omarchy 4.0.4 ("Quattro"): the desktop shell is a single long-lived
  Quickshell process — observed locally as
  `quickshell -n -p /usr/share/omarchy/shell`. The manual calls this shell
  `omarchy-shell`; note that locally `/usr/sbin/omarchy-shell` is a helper
  script, not the process itself.
- quickshell 0.3.1 (commands `quickshell`/`qs`).
- Built-in plugins: `/usr/share/omarchy/shell/plugins/` (observed: `bar`,
  `menu`, `panels`, `notifications`, `osd`, `clipboard`, `lock`,
  `dev-gallery`, …) — the reference implementations to study.
- User plugin area: `~/.config/omarchy/plugins/` (observed: `b.okomart`,
  `local.networks`, `digitalfrost84.auto-dark-mode`,
  `mangoleaf.workspace-manager`, …) — namespacing convention
  `author.plugin-name`.

## Primary sources

- Omarchy Quattro manual, shell plugins chapter:
  https://github.com/basecamp/omarchy/blob/quattro/manual/32-shell-plugins.md
- Omarchy plugin marketplace (product distribution target):
  https://plugins.omarchy.org
- Quickshell documentation: https://quickshell.outfoxxed.me (confirm exact
  docs URL at Phase 0 — OPEN).

## Rules (binding, from the skill)

1. Check current official Quattro docs AND a current official built-in
   plugin before inventing any abstraction; match official patterns.
2. QML is presentation only — no qBittorrent networking, no secrets, no
   business logic, no polling loops for data the daemon can push.
3. Shell stability first: the shell is one long-lived process; minimize
   allocations, timers, subprocesses; never block the render thread.
4. Derive visuals from the active Omarchy theme; dark/light coherence.
5. Truthful degraded states; integrate with Omarchy's global interface; no
   second navigation hub; no fake-terminal aesthetics.

## Open questions

- [OPEN] Exact registration surface for a third-party plugin in current
  Quattro (manual chapter + a real plugin to be studied at Phase 0).
- [OPEN] Product plugin namespace on plugins.omarchy.org.
