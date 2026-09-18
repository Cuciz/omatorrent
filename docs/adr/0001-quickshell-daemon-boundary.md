# ADR-0001: Quickshell is presentation only; business logic lives in a separate daemon

STATUS: ACCEPTED (2026-09-18, project bootstrap)

## CONTEXT

OmaTorrent presents itself inside the Omarchy Quattro desktop, which runs as
a single long-lived Quickshell process (`omarchy-shell`). Business logic
lived directly in shell widgets would couple product behavior to the shell
process lifecycle: a logic bug becomes a desktop crash, state is lost on
shell reload, tests require a running shell, and secrets/state handling
would sit inside a UI process. Torrent control also needs a stable owner
for background state synchronization, persistence, and backend connections
independent of whether the shell is running.

## DECISION

The Quickshell/QML layer is presentation only: it renders state delivered
by the daemon and sends user intents back. All business logic — qBittorrent
communication, state management, persistence, monitors, secrets — lives in a
separate daemon (`omatorrent-service`). QML never performs qBittorrent
network calls, never holds secrets, and never makes business decisions.

## CONSEQUENCES

- Shell stability is protected: daemon crashes/restarts degrade the UI
  instead of the desktop.
- Business logic is testable without the shell; the UI is testable against
  the IPC contract alone.
- Degrade-truthfully becomes structural: disconnected states are the normal
  QML condition when the daemon is absent.
- Cost: a second process, an IPC contract to version and test, and
  deployment of the daemon in addition to the plugin.

## ALTERNATIVES

- All logic in QML inside omarchy-shell: rejected — couples product to
  shell lifecycle, untestable headlessly, secrets in a UI process, restarts
  lose state.
- Shell-external UI (non-Quickshell window app): rejected — the product goal
  is a native Omarchy bar/panel/dashboard experience.

## EVIDENCE/SOURCES

- Omarchy Quattro shell-plugins manual (the desktop runs as a single
  long-lived Quickshell process; everything on screen is a plugin):
  https://github.com/basecamp/omarchy/blob/quattro/manual/32-shell-plugins.md
- Installed system observation 2026-09-18: Omarchy 4.0.4, quickshell 0.3.1;
  the shell process observed as `quickshell -n -p /usr/share/omarchy/shell`.
