# OmaTorrent — Quickshell / Omarchy Quattro Development Notes

Status: VERIFIED (Phase 0, 2026-09-18) — environment facts, reference
implementations studied, and pitfalls actually hit while building the
Phase 0 proof widget. Version-sensitive; re-verify per installed Omarchy.

## Confirmed environment facts (2026-09-18, this workstation)

- Omarchy 4.0.4-1 ("Quattro"): the desktop shell is one long-lived
  Quickshell process — `quickshell -n -p /usr/share/omarchy/shell`
  (pid observed in `wayland-wm@hyprland.desktop.service` cgroup; restart
  via the official `omarchy-restart-shell`).
- quickshell 0.3.1-1 (commands `quickshell`/`qs`).
- Built-in plugins: `/usr/share/omarchy/shell/plugins/`; shell source and
  plugin loader: `/usr/share/omarchy/shell/shell.qml` +
  `services/PluginRegistry.qml`.
- User plugin area: `~/.config/omarchy/plugins/`, naming `author.plugin`
  (OmaTorrent uses dev ID `local.omatorrent`; public ID OPEN).
- Plugin validation: `omarchy plugin validate <folder>` (schema 1).

## Reference components (studied for Phase 0)

1. **Bar widget contribution** — `Ui/BarWidget.qml`, `plugins/bar/Bar.qml`,
   `plugins/bar/widgets/Workspaces.qml` (all under
   /usr/share/omarchy/shell/). Pattern: manifest `kinds:["bar-widget"]` +
   `entryPoints.barWidget` → registered in `BarWidgetRegistry` → the bar's
   ModuleSlot Loaders it, injecting `bar`, `moduleName`, `settings`.
   Extend `BarWidget`, set `moduleName`, size via `implicitWidth/Height`.
   Never mark injected properties `required`.
2. **Theme tokens** — `Commons/Color.qml` + `Commons/Style.qml`
   (`import qs.Commons` — usable verbatim from third-party plugins).
   `Color.foreground/muted/urgent/accent`, `Style.font.family/body`,
   `Style.space(n)`, `Style.cornerRadius`. Dark/light switches re-evaluate
   all bindings live. No hard-coded colors anywhere in OmaTorrent QML.
3. **Buttons** — `Ui/WidgetButton.qml`: `text`, `foreground`,
   `fontFamily/fontSize`, `tooltipText`, `fixedWidth`, `vertical`
   handling; self-styling from bar tokens. The proof widget uses it
   directly (pattern from `~/.config/omarchy/plugins/jeffmtb.moon-phase/`).
4. **Third-party bar widget skeleton** —
   `~/.config/omarchy/plugins/jeffmtb.moon-phase/BarWidget.qml`: extends
   `BarWidget`, `WidgetButton` content row, tooltip, panel loader +
   `injectPanel()`, `SystemClock` refresh. Closest first-party-looking
   citizen; OmaTorrent's manifest mirrors its shape.
5. **Service/polling anti-pattern to avoid** —
   `/usr/share/omarchy/shell/plugins/panels/tailscale/Service.qml` (and
   `weather/Panel.qml`): `Process` + `Timer` polling in QML. Omarchy
   built-ins do this for simple CLI wrappers, but for OmaTorrent this is
   exactly what the daemon replaces (ADR-0001); the bar widget polls the
   local daemon socket only.
6. **Local prior art (counter-example)** —
   `~/.config/omarchy/plugins/local.networks/NetworkStrip.qml`: a panel
   that does `XMLHttpRequest` **directly to the qBittorrent WebUI from
   QML** (~lines 105–135) — the precise ADR-0001 violation OmaTorrent
   exists to avoid. Do not copy its transport; its layer-shell placement
   code is otherwise a fine reference.

OmaqBT (a torrent shell mentioned as prior art) is NOT present on this
machine; nothing could be inspected or verified — recorded as NOT
AVAILABLE, no assumptions imported from it.

## IPC client rules (verified against Quickshell 0.3.1)

- Use `Quickshell.Io` `Socket` (QLocalSocket → Unix domain on Linux):
  `path`, `connected` (settable — setting true connects), `write(QString)`,
  `flush()`, signals `connectionStateChanged`, `error`.
- Frame parsing: attach `SplitParser { splitMarker: "\n"; onRead: ... }`
  via the inherited `parser` property.
- **Pitfall 1**: `write()` does not append a terminator — send
  `JSON.stringify(msg) + "\n"` or the daemon (correctly) waits forever.
- **Pitfall 2**: an initial `connected: true` at construction does not
  fire `connectionStateChanged`; make the hello bootstrap-aware (poll
  timer checks `connected && !helloSent`). See
  plugins/local.omatorrent/BarWidget.qml.
- **Pitfall 3**: an instantiated bar widget may survive plugin-file
  rescans with stale code; `omarchy-restart-shell` gives a clean state
  after edits.
- No shell-side `Socket` usage exists upstream to copy (Omarchy moved its
  own IPC to `IpcHandler`); plugin-side Unix-socket clients are
  legitimate and match ADR-0003/0004.

## Rules (binding, from the skill)

1. Check current official Quattro docs AND a current official built-in
   plugin before inventing any abstraction; match official patterns.
2. QML is presentation only — no qBittorrent networking, no secrets, no
   business logic, no polling loops for data the daemon can push.
3. Shell stability first: minimize allocations, timers, subprocesses;
   never block the render thread; guard every `JSON.parse`.
4. Derive visuals from the active Omarchy theme; dark/light coherence.
5. Truthful degraded states; integrate with Omarchy's global interface;
   no second navigation hub; no fake-terminal aesthetics.
6. Visual rule for OmaTorrent: indistinguishable from a first-party
   Omarchy plugin — native tokens, transparent-first surfaces, native
   spacing/radii, minimal chrome, no RGB/cyberpunk decoration, no
   standalone-desktop-app look, no card-dashboard aesthetic.

## Primary sources

- Omarchy Quattro manual, shell plugins chapter:
  https://github.com/basecamp/omarchy/blob/quattro/manual/32-shell-plugins.md
- Plugin marketplace (product distribution target):
  https://plugins.omarchy.org
- Quickshell docs: https://quickshell.outfoxxed.me (types verified
  locally at /usr/lib/qt6/qml/Quickshell/Io/quickshell-io.qmltypes).

## Open questions

- [OPEN] Product plugin namespace on plugins.omarchy.org.
- [OPEN] Push vs poll for bar updates at 0.2 (IPC v1 is client-poll at
  2 s; a subscription design needs its own contract review).
