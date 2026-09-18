---
name: omatorrent-quickshell-development
description: Use whenever modifying OmaTorrent QML, Quickshell code, Omarchy Quattro plugin entry points, bar widgets, panels, overlays, or shell integration. Requires checking current official Omarchy Quattro documentation and matching a current official built-in plugin pattern before inventing any abstraction.
---

# OmaTorrent Quickshell/QML Development Rules

Applies to every change under the shell plugin: bar widget, panel,
dashboard/overlay, registration, lifecycle, keyboard interaction, theming,
performance work.

## Before writing QML — mandatory

1. **Check the current Omarchy Quattro documentation** (manual chapter on
   shell plugins) for the installed Omarchy version — plugin API surface is
   version-sensitive.
2. **Inspect a current official built-in plugin** on this machine
   (`/usr/share/omarchy/shell/plugins/`, and the user plugins under
   `~/.config/omarchy/plugins/`) for the interaction pattern you need. Match
   an existing official pattern; invent an abstraction only when no official
   pattern covers it, and say so.
3. Confirm the change keeps QML presentation-only: data comes in via the
   daemon IPC surface, events go out, nothing else.

## Hard rules

- No qBittorrent networking in QML. No secrets in QML. No business logic.
- No polling timers or subprocess spawning from QML for data the daemon can
  push or serve.
- Minimize allocations and model rebuilds; prefer incremental model updates;
  never block the render thread; the shell is a single long-lived process —
  leaks and crashes are desktop regressions.
- Derive visuals from the active Omarchy theme; keep dark/light coherence.
- Degrade gracefully: daemon unreachable renders a real disconnected state,
  never fabricated numbers.
- Integrate with Omarchy's global interface; no second navigation hub, no
  nested dashboards, no fake-terminal aesthetics.

## Verification

Validate the actual runtime result in the real shell: plugin loads, panel
opens/closes repeatedly without stale state, theme switch survives,
memory/CPU sanity observed. A file edit is not proof.
