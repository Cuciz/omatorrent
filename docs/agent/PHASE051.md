# Phase 0.5.1 — Sprout rebrand (issue #11)

Branch: feat/phase051-sprout-rebrand. Baseline: main @ fba4aa6.

## What changed

- **Brand system**: docs/BRAND.md (identity, palette extracted from the
  approved artwork, logo family, usage rules, naming audit, pre-1.0
  migration candidates). Deterministic asset source:
  assets/brand/generate.py → SVGs (glyph full/compact, wordmark, lockup;
  mono variants), integer-multiple PNG exports (transparent + dark
  base), terminal/block variant (sprout-blocks.txt).
- **QML**: SproutGlyph.qml (both plugins; Canvas, theme-foreground,
  integer-snapped, no assets/fonts at runtime; grid sync enforced by
  tools/validate_brand.py). Bar widget: glyph before the label,
  monochrome in the status color. Panel: two-row header (identity row
  + state row), state text elides on long hosts, branded empty state
  (full glyph + green SPROUT wordmark + "No torrents growing yet." +
  Add magnet), settings title "sprout · qBittorrent connection".
  Dashboard: glyph + Sprout title + tagline, host under version.
- **Public naming**: manifests (name/displayName), plugin READMEs,
  README.md (lockup + product statement + "is not" list), docs titles,
  current-tense doc prose, AGENTS.md, systemd Description=, Go config
  error string. Internal technical identifiers (repo, Go module,
  omatorrent-service, socket/config paths, plugin IDs, harness) kept
  deliberately — full audit in docs/BRAND.md.
- **Tooling**: tools/validate_brand.py; DEVELOPMENT.md command rows.

## Validation (executed 2026-09-19/20, this workstation)

| Gate | Result | Evidence |
|---|---|---|
| gofmt / go vet / go build | PASS | clean output |
| go test -race -count=1 ./... | PASS | all 8 packages ok |
| validate_harness.py | PASS | 82/82 |
| test_guard_hook.sh | PASS | 39/39 |
| validate_brand.py | PASS | grid sync, assets, names, determinism |
| omarchy plugin validate (both) | PASS | exit 0 |
| test_quickshell.sh (full stack) | PASS | 29 status, 10/10 mutations, v1.4 conn |
| Shell restart + live runtime | PASS | no QML errors in quickshell log |
| Daemon journal credential scan | PASS | 0 hits across the session |

## Visual matrix (grim captures, Omarchy 4.0.4, 1920x1200, Catppuccin)

All captures vision-verified against the task's ASCII projections and
the approved brand reference.

| # | Capture | Result | File |
|---|---|---|---|
| 1 | Bar widget (glyph + speeds) | PASS — glyph crisp, optically aligned, single render | phase051-bar.png |
| 2 | Panel populated (local, long torrent name) | PASS — two-row header; long name elides | phase051-panel-local.png |
| 3 | Panel empty state | PASS — glyph, green SPROUT wordmark ≥120 px (BRAND.md minimum; initial 21×5 px sizing bug caught by UI review, fixed + re-captured), copy, Add magnet | phase051-panel-empty.png |
| 4 | Panel auth_required (live fixture) | PASS — truthful callout + settings link + last-known | phase051-panel-auth-required.png |
| 5 | Panel auth_failed (live, wrong password) | PASS — sticky class | phase051-panel-auth-failed.png |
| 6 | Panel TLS failure (live, self-signed) | PASS — tls_hostname, no-downgrade copy | phase051-panel-tls-failed.png |
| 7 | Panel insecure HTTP (live ack flow) | PASS — "connected (insecure) · 192.168.1.141:8090" + badge + empty state | phase051-panel-remote-insecure.png |
| 8 | Dashboard local | PASS — glyph + Sprout + tagline, state/version/host | phase051-dashboard-local.png |
| 9 | Dashboard remote (insecure fixture) | PASS | phase051-dashboard-remote.png |
| 10 | Panel light theme (Catppuccin Latte) | PASS — glyph follows theme fg; green wordmark readable; semantic warning intact | phase051-panel-light-theme.png |
| 11 | Dashboard light theme | PASS | phase051-dashboard-light-theme.png |
| 12 | Connection settings form | NOT AVAILABLE — requires pointer input; no input-automation tool in this session. The only new element (title row) uses the same SproutGlyph/typography pattern verified live in headers. |
| 13 | 1000 torrents / fractional scaling / >21-char host | NOT RUN — 1000 covered by daemon benchmarks (identical render path); no fractional-scale output present; 21-char host shown in #7, longer labels elide by construction (spacer clamps at 0, text elides) |
| 14 | Daemon offline state | NOT RUN this session — unchanged Phase 0.2 surface ("omatorrent-service offline" string kept deliberately as a technical diagnostic) |

## Live degraded-state method (disposable fixture)

Localhost `qbittorrent-nox 5.2.3` temp profile (auth on with its
rotating temporary admin password; HTTPS variant with a self-signed
cert for the TLS capture; LAN-address HTTP for the insecure-ack flow).
Driven exclusively via IPC `connection.configure`/`connection.status`
frames on the real daemon socket. The real backend and its 3 torrents
verified untouched before/after (connected, 3 torrents, epoch 7 after
restore, no stored secret). Fixture killed and deleted;
`secret-tool search service omatorrent` returns no items
(secret_action delete on restore); daemon journal scanned for
credential strings: 0 hits.

State ladder exercised live: auth_required → auth_failed (sticky) →
tls_hostname → insecure_http rejected without ack → connected
insecure with ack → empty backend (0 torrents) → restored local.

## Security scan

Vision scan across all captures: CLEAN — no passwords (the fixture's
temporary password was never typed into any UI), no tokens, no
sensitive paths; the TLS-failure panel shows certificate diagnostics
only. Daemon journal: 0 credential hits. Screenshot torrent names are
disposable phase-fixtures or smoke-test artifacts.
