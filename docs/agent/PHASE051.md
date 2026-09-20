# Phase 0.5.1 — Sprout rebrand (issue #11)

Branch: feat/phase051-sprout-rebrand (merged and deleted). Baseline:
main @ fba4aa6. **MERGED: PR #13, approved head 6fceae6, merge commit
d1a0eec592b5b3699cb4c2521073987aa2ba5166 (2026-09-20); issue #11
closed on merge. Phase 0.5.1 COMPLETE — public product identity is
Sprout. Next milestone: Phase 0.6 advanced torrent controls (not
started).**

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
| 3 | Panel empty state | PASS — deterministic pixel proof (brand-green count in the thousands; method: magick -fuzz 12% opaque count) + open-prompt vision check; TWO bugs fixed en route: wordmark sized 21×5 px (UI review) and a zero-height ListView collapse that made every empty/degraded list message invisible since Phase 0.2 (see below) | phase051-panel-empty.png |
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

## Independent review rounds (read-only subagents)

**Round 1** (verdicts on the pre-fix tree):
- Architecture: **APPROVE** with 6 P3 findings — all applied
  (QUICKSHELL.md no-hardcode rule qualified; lowercase header casing
  documented in BRAND.md; `__pycache__` gitignored; generate.py grid
  comments corrected; manifest descriptions de-noised; versions bumped
  to 0.5.1).
- Quickshell/UI: **BLOCK**, two findings — both fixed in `ba427b0`:
  1. Empty-state wordmark rendered 21×5 px (`Style.space` unit
     miscalculation) — now 29 px tall ≈ 121 px wide, meeting BRAND.md's
     120 px minimum (full glyph raised to 16 px); re-captured.
  2. `SproutGlyph.qml` default color referenced nonexistent
     `Style.foreground` — now `Color.foreground` (both copies synced).
  Nits applied too (bar label color animation, dashboard host elide,
  BRAND.md font-tracking exception for inline header glyphs).
- QA: **BLOCK** — evidence incomplete at review time (PHASE051.md not
  yet committed; screenshots missing from the tree).

**Round 2** (QA re-review at `ba427b0`): **BLOCK** — ten of the eleven
screenshots referenced by the visual matrix were still absent from the
repository, and `ba427b0`'s commit message claimed them present. Root
cause: the captures were lost from the working tree between the cp
batch and the commit (the first PHASE051.md "reviews" edit also
silently no-oped against a nonexistent header). This round re-took
every capture directly into `docs/screenshots/`, verified each with
`git ls-files docs/screenshots | grep -c 051` = 11 before committing,
and this section records both the review history and the earlier
premature claim so the history reads truthfully.



## Stale-panel investigation (issue #12, deterministic)

Report: after switching the daemon from a populated backend A to an
empty backend B, the open panel allegedly showed "Nothing in this
filter" instead of the branded empty state; a shell restart showed the
correct state.

Wire-level investigation with a long-lived frame-logging subscriber:

| Step | Frames received by the long-lived subscriber | Panel visual |
|---|---|---|
| A (3 torrents) → B (empty), clean switch | `delta removed=[all 3]` at t+3.0s | branded empty state (correct) |
| B → A round trip | `delta changed=[3]` at t+19.3s | 3 rows again (correct) |
| A → auth-required fixture (degraded) | switch removals per SwitchBackend | auth callout, empty list (correct — last-known does not survive a reconfigure-switch) |
| degraded → recovered, same URL | none (empty → empty = no diff) | consistent |

Daemon audit (syncer.SwitchBackend / commitDegradedEpoch / subscription
pump): publish ordering and semantics correct. The report did NOT
reproduce in any path; its only evidence was a screenshot transcribed
via a leading vision prompt — a method proven unreliable in this same
session (one capture's leading-prompt transcript was contradicted by an
open-ended re-check). Classification: not reproduced / probable
transcription artifact (issue #12).

Genuine findings: (1) `Panel.qml resetSession()` did not clear
`applyingSnapshot` — a snapshot interrupted by a disconnect left the
flag stale-true so later deltas skipped view rebuilds until the next
completed snapshot; fixed here (one line, QML-only, truthful-display
hardening). (2) Documented behavior: the IPC server closes connections
idle for 30 s (`idleReadDeadline`) — shell widgets never hit it (2 s
polls), custom subscribers must resubscribe on reconnect.

## Found during final verification: empty-state overlays never rendered (fixed)

The UI re-review's deterministic pixel check proved the staged
"empty" capture contained no empty state at all — the panel body was
blank. Root cause (Panel.qml): the empty/degraded messages were
children of the torrent ListView, whose height was bound to
`Math.min(contentHeight, 340)`. At `count === 0`, contentHeight is 0,
so the ListView collapsed to zero height and swallowed its centered
children — meaning even the pre-0.5.1 "No torrents" message could
never have rendered (present since Phase 0.2; never noticed because
the dev backend always had torrents). Fix: the list area is now a
fixed-height Item; the ListView fills it; the brand empty state,
filter-empty message, and degraded message are overlays on the area.
Verified live: brand-green pixel count (magick: -fuzz 12% opaque
count for #A1D06A) — 4,922 px in the working capture, 4,057-4,062 px
in the final committed framing (crop geometry affects the absolute
number; the method is the check, any count in the thousands with six
letter clusters is the wordmark) — plus open-prompt vision
confirmation of glyph/wordmark/copy/Add-magnet; populated panel
re-captured on the same build.

This also retroactively explains the issue #12 report: "Nothing in
this filter" could not have been visible in the list area either —
further support for the transcription-artifact classification.

The earlier session notes claiming the empty state "verified" before
this fix were vision-tool hallucinations under leading prompts; all
current evidence is deterministic (pixel counts) or open-prompt.

## Final review round (pushed HEAD 891030c, after re-verification)

All four independent reviews re-ran against the pushed HEAD and
returned **APPROVE**:
- Architecture: APPROVE (P3: record the pixel-count method — done
  above; P4s: validate_brand regeneration writes assets, delegate
  indentation, pixel-S/G legibility note).
- Quickshell/UI: APPROVE (deterministic letter-cluster analysis of the
  wordmark; confirmed the restructure, hardening, and all prior fixes;
  advisory: capture framing cut the panel's left edge at this theme's
  1.5 spacing scale — fixed in the final framing pass below).
- Brand: APPROVE 10/10 (pixel-level verification of every asset; two
  doc nits fixed below: lockup 94x18 not 93x18, generate.py docstring
  filename).
- QA: APPROVE (11/11 screenshots binary-identical at HEAD; empty-state
  count independently reproduced; public-name, identifier, journal,
  theme, and disposition checks all pass; advisory: ambient desktop
  content beside the panel — fixed by the tighter framing).

Final framing pass (advisory fixes): all panel captures re-cropped to
the full popout (590x640; the panel is 540 px wide at this theme's
1.5 spacing scale) and dashboards to the card (880x950), removing
ambient desktop content from a public repo.

## External-review follow-up: list-area sizing regression (fixed)

The external review of PR #13 caught a regression introduced by the
zero-height fix: the list area had become a permanently max-height
(340-unit) block, so healthy panels with 1-3 torrents wasted a large
blank body — a break from the native compact-panel behavior.

Fix: the list area is now content-driven while rows exist
(`Math.min(listView.contentHeight, Style.space(340))`) with a
100-unit floor when `view.count === 0` (the only state that needs
it — degraded WITH last-known rows keeps content sizing so the rows
stay visible under the centered diagnostic, as before). The
KeyboardPanel window height follows the same content
(`Style.space(120)` chrome + live list height + conditional rows),
so few torrents mean a compact panel again.

Measured card heights (connected-component analysis of the card
surface, theme scale 1.5, deterministic):

| State | Card size (px) | Rule |
|---|---|---|
| 0 torrents (healthy empty) | 534×324 | overlay floor |
| 1 torrent | 534×194 | content-driven |
| 3 torrents | 534×239 | content-driven |
| 34 torrents | 534×684 | capped (340-unit list) + scroll |
| degraded, 1 last-known row | 534×194 | content-driven + centered diagnostic |

Zero-result filter: not runtime-capturable this session (selecting a
filter needs pointer input; no input-automation tool installed) — it
shares the overlay floor with the healthy-empty state through the
identical `view.count === 0` branch, whose rendering is proven above
and in the committed captures. All seven panel screenshots were
re-taken against the adaptive build; the empty state's brand-green
count re-verified (4,922 px). Vision checks (open prompt): 3-torrent
panel compact, brand empty state fully visible, degraded diagnostic
visible with the last-known row beneath.
