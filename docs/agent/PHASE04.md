# Phase 0.4 — Native dashboard (record)

Tracking: GitHub issue #7; branch `feat/phase04-dashboard`.
Status: IMPLEMENTED, PR open for external review (NOT merged). Date: 2026-09-19.
Base: main @ c487e2d (Phase 0.3 merged at fc8615f).

## Delivered

```
Torrent panel (header button ▤, first-party bar.run shell toggle)
    ↓ `omarchy-shell shell toggle local.omatorrent-dashboard`
Dashboard overlay plugin (local.omatorrent-dashboard, overlay kind,
  not keepLoaded → destroyed on close; emojis/clipboard skeleton)
    ↓ IPC v1.3 (ADR-0007: dashboard.status poll, one in flight, id-matched)
daemonHandler.Dashboard() → state.Aggregate(committed State)
  (O(N + A log A): counts, saturating byte sums, top-5 active —
  one pass over N torrents plus a sort of the A active candidates,
  worst case O(N log N))
syncer commits free_space_on_disk (last-known-good, dropped degraded)
```

- **IPC v1.3** (ADR-0007, docs/IPC.md): `dashboard.status` request
  (exact two keys like health) with two response shapes — live (exact
  key set; `free_space` present iff the backend reported it, 0 is real)
  and degraded (`qbittorrent:"unavailable"` + optional `last_known`
  iff a cycle ever committed; speeds/active never fabricated). Frame
  budget enforced by name-halving then trailing-entry drops
  (counts.active stays truthful). v1.0/v1.1/v1.2 shapes untouched;
  protocol major stays 1. Contract fixtures: dashboard-status.txt +
  three response fixtures, byte-pinned by tests.
- **Daemon**: `state.Aggregate` (internal/state/dashboard.go) —
  classification semantics identical to the panel filters (downloading/
  seeding/paused normalized-state equality; completed = progress ≥ 1;
  active = dlspeed>0 ∥ upspeed>0); saturating int64 sums; per-torrent
  remaining clamp; top-5 active by combined speed desc, name asc.
  `free_space_on_disk` committed with last-known-good discipline;
  nil on degradation (unknown ≠ zero).
- **Dashboard UI** (plugins/local.omatorrent-dashboard/Dashboard.qml):
  first-party overlay skeleton (PanelWindow, Overlay layer, Exclusive
  focus while open, scrim, centered BorderSurface card, Color.menu/
  Style tokens only). Sections: header (state + versions), live
  transfer, torrent counts, current data (+ overall completion track),
  transferring-now list (≤5), footer (open panel / Esc). Degraded
  states: urgent callouts (agents style); live sections HIDDEN while
  degraded (never zeroed); last-known data dimmed + labeled; version
  line suppressed while the daemon is offline.
- **Why a separate plugin** (not an `overlay` kind on
  local.omatorrent): `shell.qml isBarWidgetPanelPlugin()` returns false
  for plugins that also carry panel/overlay/menu kinds — adding one
  would silently reroute `omarchy-shell shell toggle local.omatorrent`
  from the bar-widget panel to the shell's panel loader. The separate
  companion overlay is the exact omarchy.menu model (menu =
  ["menu","bar-widget"] + companion summon). Recorded in
  docs/ARCHITECTURE.md as-built and docs/QUICKSHELL.md.

## Dashboard data model (every field: one real source)

| Field | Definition | Source | Measured/derived | Degraded behavior |
|---|---|---|---|---|
| header: daemon offline/connected | IPC socket + frame freshness | client connection state | measured | the state itself |
| header: qBittorrent vN · WebAPI N | backend probe strings | State.AppVersion/WebAPIVersion via daemon probes (once per backend epoch) | measured | hidden while daemon offline; last_known versions while qB degraded |
| dl_speed / up_speed | current global speeds, B/s | sync/maindata server_state (committed cache) | measured | omitted (section hidden) — never zeroed |
| counts.total | torrents in committed state | len(Torrents) | derived | last-known |
| counts.active | dlspeed>0 ∥ upspeed>0 | committed per-torrent speeds | derived | carried in last_known (wire); UI shows it only in the live section |
| counts.downloading/seeding/paused | normalized-state equality (ADR-0005 set) | committed states | derived | last-known |
| counts.completed | progress ≥ 1 (panel filter predicate) | committed progress | derived | last-known |
| aggregate.total_size/completed_bytes | saturating sums of size/completed | committed per-torrent values | derived | last-known |
| aggregate.remaining_bytes | saturating Σ max(0, size−completed) | committed per-torrent values | derived | last-known |
| active[] (≤5) | transferring now: combined speed desc, name asc | committed per-torrent speeds | derived | omitted |
| free_space | server_state.free_space_on_disk — free space on the disk of the DEFAULT save path (WebAPI ≥ 2.1.1; multi-path setups not reflected) | committed State.FreeSpace | measured | omitted (nil on degradation) |

No other metrics are displayed. No history, averages-over-time, or
persisted data exists in this phase (all 0.8).

## Evidence summary (validation matrix)

Executed 2026-09-19 on this workstation (daemon 0.4.0-phase04
deployed via user systemd unit; plugin deployed to
~/.config/omarchy/plugins/):

| Check | State | Evidence |
|---|---|---|
| gofmt / go vet / go build | PASS | clean output, repo tree |
| go test -race -count=1 ./... | PASS | all packages ok (incl. new ipc dashboard + state aggregate/free-space suites) |
| Dashboard unit/contract tests | PASS | TestDashboardStatus{RequestGrammar,OKShape,DegradedShapes,FrameBudget,AvailableAlongsideSubscription}, TestDashboardContractFixtures, TestDashboardFrameSizeIndependentOfPopulation, TestAggregate* (9), TestFreeSpaceCommitAndDegraded |
| Contract fixtures byte-pinned | PASS | encoder output == fixtures for all three response shapes |
| Harness validator | PASS | 82/82 |
| Guard hook | PASS | 39/39 |
| omarchy plugin validate (both plugins) | PASS | exit 0 |
| Quickshell smoke (incl. v1.3 + v1.2 regression) | PASS | dashboard=1 dash-model=1 subscribed=1 mutation-stages=10/10 failures=0 (final run; schema exact-key check, aggregate invariants, counts.total == v1.1 snapshot items cross-check) |
| Benchmark L1: state.Aggregate only | MEASURED | 10 → ~7.1 µs · 100 → ~94 µs · 1000 (all-active worst case) → ~1.33 ms/op, 181 KB/op, 22 allocs (median of 3, no -race) |
| Benchmark L2: syncer.State() + Aggregate | MEASURED | 10 → ~10.3 µs · 100 → ~146 µs · 1000 → ~2.47 ms/op, 476 KB/op, 24 allocs (adds the RLock + torrent-map clone) |
| Benchmark L3: full dashboard.status response (State + Aggregate + adaptation + EncodeDashboardStatus) | MEASURED | 10 → ~24 µs · 100 → ~151 µs · 1000 → ~2.53 ms/op, 480 KB/op, 37 allocs (cmd-level benchmark; excludes socket I/O) |
| IPC payload vs population | MEASURED | ok-frame 830/838/846 B at 10/100/1000 torrents (digit-width deltas only; O(1) by design); worst-case pathological frame 2486 B < 4096 budget (TestDashboardStatusFrameBudget at the final reviewed encoder; supersedes the earlier CJK-premise 1781 B and the external-review replica 3319 B figures) |
| QML dashboard open/close/render | PASS | live screenshot (docs/screenshots/phase04-dashboard-live.png) — header/transfer/counts/data/free-space/footer all rendering real values |
| Escape close / click-outside code path | PASS | 2× open→Escape cycles after fix, zero journal warnings |
| Degraded: daemon unavailable (A) | PASS | screenshot phase04-dashboard-daemon-degraded.png — urgent callout, sections hidden, bar qBT OFFLINE |
| Recovery after daemon restart (E) | PASS | screenshot phase04-dashboard-recovered.png — connected, live data back (reconnect backoff ≤ 10 s) |
| Initial loading (C) | PASS (transient) | bare degraded shape unit-tested + rendered pre-first-sync state exercised at daemon restart; sub-second in practice |
| Normal connected (D) | PASS | live screenshot |
| Degraded: qBittorrent down, daemon up (B) | NOT OBSERVABLE live | qbittorrent-nox is a system service (sudo unavailable); shape/guards covered by unit tests + smoke dash-model; rendering path proven via the daemon-degraded cycle |
| 50× open/close lifecycle torture | PASS | daemon fd count 8→8, socket peers 0–1 across 50 toggles; zero QML errors; destroy-on-close overlay |
| Shell restart → reopen | PASS | renders connected post-restart; 3 bounded peers (bar+panel+dashboard) |
| Phase 0.2 regression | PASS | smoke snapshot + status exchanges green |
| Phase 0.3 mutation regression | PASS | smoke 10-stage lifecycle incl. both delete_files paths, replay, duplicate, invalid, stale |
| v1.0/v1.1/v1.2 compatibility | PASS | contract tests + fixtures unchanged; dashboard.status available alongside subscriptions |
| Theme dark | PASS | live/degraded/recovered screenshots (Catppuccin dark) |
| Theme light + accent | PASS | phase04-dashboard-light-theme.png (Catppuccin Latte; accent/urgent re-derived from tokens) |
| Multi-monitor placement | NOT RUN | single-display workstation; overlay follows first-party behavior (maps on default output; no per-screen copies) |
| Secret scan / git diff --check / status | PASS | clean |
| Journal audit across all cycles | PASS | zero OmaTorrent errors (one fixed defect below; unrelated local.networks/portal noise excluded) |

## Defects found and fixed during validation

1. `close()` called `shell.hide()` → host invoked `close()` again →
   unbounded recursion (observed live: "Maximum call stack size
   exceeded"). Fixed with the first-party close/dismiss split
   (host-invoked close only flips visibility; user-initiated dismiss
   notifies the host). Re-verified clean.
2. Stale version line while daemon offline (observed in first
   degraded screenshot): versions now suppressed unless daemonUp.
3. Missing `import Quickshell.Wayland` (WlrLayershell attached object
   warning) — surfaced during first deploy; fixed.
4. Hot-reload staleness: first summon after a plugin rescan can run a
   stale component (documented as Pitfall 3 hardening in
   docs/QUICKSHELL.md; clean restart avoids it).

## Performance statement

Categories kept strictly separate:

- MEASURED (exact benchmark targets; fixtures all-active — A = N, the
  sort's worst case; medians of 3 runs, race detector OFF, i5-1334U
  under a live desktop, load average ~1.3):
  - L1 `BenchmarkDashboardAggregate` — state.Aggregate ONLY (excludes
    the State() clone and IPC encoding): 10 → ~7.1 µs · 100 → ~94 µs ·
    1000 → ~1.33 ms/op.
  - L2 `BenchmarkDashboardStateAndAggregate` — syncer.State() (RLock +
    torrent-map clone) + Aggregate: 10 → ~10.3 µs · 100 → ~146 µs ·
    1000 → ~2.47 ms/op.
  - L3 `BenchmarkDashboardStatusResponse` (cmd) — the full per-request
    path minus socket I/O: State() + Aggregate + daemonHandler
    adaptation + EncodeDashboardStatus: 10 → ~24 µs · 100 → ~151 µs ·
    1000 → ~2.53 ms/op, 480 KB, 37 allocs.
  - IPC ok-frame 830/838/846 B at 10/100/1000 torrents (O(1) in
    population); worst-case pathological frame 2486 B < 4096 budget
    (TestDashboardStatusFrameBudget, final encoder).
  - Daemon fd/socket counts flat across 50 open/close cycles.
- ANALYZED (not measured): aggregate complexity is O(N + A log A)
  (worst case O(N log N)); QML cost is O(1) in torrent count — the
  dashboard renders ≤5 active rows from one ~840 B frame, holds no
  per-torrent model; timer cadence is 2 s poll while open, zero while
  closed (component destroyed); no QML timings were taken.
- ESTIMATED (arithmetic from the L3 measurement, not separately
  measured): one open dashboard polling at the documented ≤1 Hz costs
  at most ~2.5 ms per second of one core (~0.25%) at 1000 all-active
  torrents — worst case; typical states (few active) sit near the L1
  floor. dashboard.status answering performs no qBittorrent I/O
  (cache-served, same rule as system.status).

## Known limitations

- Multi-monitor behavior not exercised (single display): the overlay
  maps on the default output (first-party overlay behavior) — NOT RUN.
- qBittorrent-degraded live screenshot NOT OBSERVABLE (system service
  requires sudo; unit/smoke/model coverage only).
- Panel header button CLICK not directly exercised (no synthetic
  pointer available); the button's exact command path
  (`omarchy-shell shell toggle local.omatorrent-dashboard`) was
  exercised 60+ times via CLI, and the button renders (screenshot
  phase04-panel-entry.png).
- free_space reflects only qBittorrent's default save path
  (documented in UI label + QBITTORRENT.md).
- counts do not sum to total (queued/checking/error/moving/other are
  in total only) — documented in IPC.md; by design.

## Review verdicts + fixes applied

- **Architecture: APPROVE-WITH-NOTES.** All 8 mandated criteria verified
  (presentation-only QML, committed-state truth, no history, no scope
  creep, additive IPC, first-party patterns incl. the separate-plugin
  reasoning confirmed against the installed shell.qml, single-source
  metrics, docs byte-accurate). Findings fixed:
  1. MAJOR footer navigation dead under the PluginShellApi capability
     gate (an overlay plugin cannot shell.summon another plugin) →
     replaced with the documented host CLI
     (`Util.execDetached("omarchy-shell shell summon …")`);
  2. MAJOR encoder entry-drop refill could index past a truncated slice
     (daemon crash class) + version strings uncapped on the wire →
     refill loop fixed, versions capped (64 runes) in v1.3 AND v1.0
     encoders, entry cap enforced (5) in the encoder;
  3. MINOR dead constant removed;
  4. MINOR PHASE04 data-model drift (counts.active) corrected.
- **Security: PASS-WITH-FINDINGS.** All 9 checklist areas CONFIRMED-SAFE
  (no secrets/names beyond the strictly-smaller v1.3 surface vs v1.1,
  empirical NaN/Inf/overflow rejection, bounded resource use, no
  mutation surface, fixed-literal bar.run only). Findings fixed:
  F1/F3 encoder budget fail-open + version caps (as above), F2 dead
  constant, F4 budget-test premise (control runes, not CJK) corrected,
  F5 remaining-bytes positive-wrap → compare-first saturating subtract
  (+ extreme-value test). F6 (mustMarshal panic reachability) is
  pre-existing since v1.0, verified unreachable via decode — no action
  in 0.4. F7 informational.
- **QA: READY-FOR-REVIEW.** All 12 checks independently reproduced PASS
  (full race suite, dashboard unit/contract tests, benchmarks 10/100/
  1000, harness 82/82, guard 39/39, plugin validate incl. negative
  control, smoke incl. 10-stage mutation regression, 10-cycle
  open/close torture with fd 9→10→9 and socket 2→3→2, daemon-restart
  recovery screenshot verified, journal clean, repo hygiene). Zero
  defects; one environmental journal observation (a failed non-interactive
  sudo attempt from this session's qBittorrent-degraded probe — no
  effect, documented above as NOT OBSERVABLE).
