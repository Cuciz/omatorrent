# Sprout — Brand & Naming

Status: PHASE 0.5.1. This file is the durable brand specification and
the record of the OmaTorrent → Sprout naming decision. The approved
visual reference is the user-provided Sprout artwork (September 2026);
all geometry below is a clean-grid reconstruction of it (see
`assets/brand/generate.py`, the deterministic source of truth).

## Identity

- **Public product name:** Sprout
- **Tagline:** Torrent client for Omarchy
- **Old working name:** OmaTorrent (historical; do not use in new
  user-facing copy — see "Historical name" below)

The product equation is **95% Omarchy / 5% Sprout**: Sprout is a native
Omarchy plugin, not a standalone application with its own design
system. Runtime UI uses Omarchy theme tokens (colors, spacing,
typography, controls) for everything; Sprout identity appears only as
the product mark, wordmark, and a restrained green accent.

## Logo family (one geometry, three assets)

All three are generated from the same grids in `assets/brand/generate.py`.

| Asset | Grid | Use |
|---|---|---|
| Compact glyph | 12×10 | bar widget, panel header, dashboard header, 16–32 px |
| Full glyph | 24×18 | empty state, README, larger brand contexts |
| Wordmark | letters 8 modules tall | empty state title, README |
| Lockup (glyph + wordmark) | 94×18 units | README, presentations |
| Terminal/block variant | `sprout-blocks.txt` | docs, terminal presentation |

Construction facts (from the approved artwork): letters are **8 modules
tall, 5 modules wide (O: 6, T: 5 full-width bar)**, strokes 2 modules,
counters 1 module, letter gap 1 half-module; top bars cut 1 cell in
from the left (O/T symmetric). The glyph is two asymmetric leaves with
inner notches and a central stem; the stem extends one half-module
below the letter baseline.

QML runtime does **not** load the raster/SVG files:
`plugins/local.omatorrent/SproutGlyph.qml` embeds the compact grid and
renders it with the active theme's foreground color
(`tools/validate_brand.py` asserts the grid stays in sync).

## Palette (extracted from the approved artwork)

| Role | Hex | Use |
|---|---|---|
| Sprout green | `#A1D06A` | primary brand accent: glyph/wordmark in brand contexts, selective active accent |
| Warm orange | `#EDC110` | secondary detail accent only (tagline-leaf detail, seed mark); never a status color |
| Dark base | `#141415` | logo background in documentation assets |
| Light foreground | `#D9D9DA` | bitmap text / monochrome variant |

Runtime rule: **prefer Omarchy theme tokens whenever a token exists.**
Brand green may appear only on the compact glyph contexts listed above
and as a selective active-state marker (active filter, healthy-status
mark) where it composes cleanly with the theme. Do not repaint
widgets green; the plugin must remain correct under any Omarchy theme.

**Brand color ≠ status color.** Warning/error/offline/TLS-failure/
insecure-HTTP semantics keep their existing Omarchy semantic colors.
Authentication failure is never rendered in brand green.

## Usage rules

- Name casing: **Sprout** in titles, manifests, tagline and prose;
  the compact panel header/settings title use lowercase **sprout** (a
  deliberate compact-header style; the dashboard title stays Sprout).

- Clear space: ≥ 1 glyph-height around the lockup; ≥ 0.5 glyph-height
  around the compact glyph.
- Minimum sizes: compact glyph 16 px and wordmark/lockup 120 px wide
  apply to **standalone logo use** (README, favicon, presentations).
  Inline header glyphs (bar widget, panel/dashboard headers) instead
  **track the surrounding font size** — the Omarchy-native idiom that
  keeps the bar compact and optically aligned with Nerd Font icons;
  the empty-state wordmark renders ≥ 120 px wide at default scale.
  Terminal variant ≥ 80-column terminal.
- Monochrome: the glyph and wordmark must stay recognizable in a
  single foreground color (mono variants exist for both). In the bar
  and panel headers the glyph always renders in the theme foreground
  of that surface, following the surrounding text color.
- Typography: normal UI text uses Omarchy typography exclusively.
  Bitmap/wordmark typography appears only in the empty-state title and
  brand/documentation contexts — never in buttons, labels, or lists.
- The plant metaphor is allowed **only** in the empty state copy
  ("No torrents growing yet."). Torrent terminology (seeding, peers,
  download) stays technically correct everywhere else.

## Forbidden treatments

No gradients, blur/filters, rounded app-tile backgrounds, neon/glow,
glossy macOS-style icon variants, eco/leaf decoration in normal UI,
green backgrounds, wordmark in the bar, emoji as the glyph.

## Naming boundary (the audit, 2026-09-19)

Public surfaces say **Sprout**. Technical identifiers keep
**omatorrent** deliberately: they are ABI/storage/service identities,
and renaming them creates migrations, compatibility aliases, and
rollback complexity with zero user value (issue #11).

| Occurrence | Category | Action |
|---|---|---|
| Plugin display names, bar-widget displayName (manifests) | PUBLIC BRAND | RENAMED (0.5.1) |
| Panel/dashboard/settings headers, empty state | PUBLIC BRAND | RENAMED (0.5.1) |
| README.md + plugin READMEs + docs titles | PUBLIC BRAND | RENAMED (0.5.1) |
| Go config error mentioning "OmaTorrent's connection settings" | PUBLIC BRAND | RENAMED (0.5.1) |
| systemd unit `Description=` | PUBLIC BRAND | RENAMED with technical name kept in parentheses |
| Tagline "Torrent client for Omarchy" | PUBLIC BRAND | ADDED (0.5.1) |
| Repo `Cuciz/omatorrent` | INTERNAL | KEEP (MIGRATE BEFORE 1.0, needs redirect) |
| Go module path `github.com/Cuciz/omatorrent/...` | INTERNAL | KEEP (MIGRATE BEFORE 1.0) |
| Daemon/binary `omatorrent-service` | INTERNAL ABI | KEEP |
| Socket `$XDG_RUNTIME_DIR/omatorrent/service.sock` | INTERNAL ABI | KEEP |
| Config `~/.config/omatorrent/` | INTERNAL ABI | KEEP |
| systemd unit file `omatorrent-service.service` | INTERNAL ABI | KEEP |
| QML plugin IDs `local.omatorrent`, `local.omatorrent-dashboard` | INTERNAL ABI | KEEP (MIGRATE BEFORE 1.0 with compat alias) |
| `moduleName`, `omarchy-shell shell summon/toggle` arguments | INTERNAL ABI | KEEP |
| WlrLayershell namespace `omatorrent-dashboard` | INTERNAL ABI | KEEP |
| Go package dirs, import paths | INTERNAL | KEEP |
| Daemon log lines / IPC error text naming `omatorrent-service` | INTERNAL (diagnostics) | KEEP — truthful process name |
| UI degraded-state strings naming `omatorrent-service` | INTERNAL (diagnostics) | KEEP — brand must not reduce diagnostic clarity |
| ZCode dev harness (`omatorrent-dev-harness`, skills, agents, commands) | INTERNAL (dev tooling) | KEEP — installed harness would break |
| tools/ scripts, socket paths, test fixtures | INTERNAL | KEEP |
| QML file-head comments | INTERNAL (developer-facing) | RENAMED to Sprout where they describe the product (0.5.1) |
| ADRs, docs/agent/ histories, ROADMAP phase history | HISTORY | KEEP AS-IS |
| Phase 0.x screenshots (docs/screenshots/) | HISTORY | KEEP AS-IS (phase evidence) |
| `Documentation=` URL in systemd unit | INTERNAL | KEEP until repo rename |

### Historical name

Historical documents describing "OmaTorrent Phase 0.x" remain as
written. Where a doc must bridge old and new names, use: `Sprout
(formerly the OmaTorrent working name)` — only in migration/history
contexts. Normal user-facing UI says just "Sprout".

### Pre-1.0 technical migration candidates (0.9 or dedicated milestone)

1. Repository rename `Cuciz/omatorrent` → `Cuciz/sprout` (GitHub
   redirect covers old URLs; update `Documentation=`, README badges).
2. Go module path + internal import paths (single coordinated commit).
3. Daemon binary/unit `omatorrent-service` → requires config + socket
   path migration and a compat window; highest-risk item.
4. QML plugin IDs `local.omatorrent*` → requires Omarchy plugin
   registry compat alias; blocked until public marketplace ID decision
   (ADR-0004 leaves the public ID OPEN).
5. Config/runtime directories (`~/.config/omatorrent`,
   `$XDG_RUNTIME_DIR/omatorrent`) — migrate together with (3).
