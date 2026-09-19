# OmaTorrent — Testing & Evidence

Status: PHASE 0.5 — suites exist and were executed (evidence in
docs/agent/PHASE0..PHASE05.md). Evidence rules used by /ot-verify, the
omatorrent-verification skill, and the qa-release agent.

## States (project vocabulary)

PASS / FAIL / NOT RUN / NOT AVAILABLE / BLOCKED — defined in the
omatorrent-verification skill. A requirement is complete only with recorded
evidence; "code looks right" is never sufficient.

## Suite layout (as built in Phase 0)

- **Sync/state (0.2)** — `internal/state/syncer_test.go`: rid=0 full
  update, incremental changed/added/removed, empty delta, malformed
  delta AND full payloads preserve last-known-good, backend-restart
  rebuild (no ghosts, correct rebuild delta), degraded/recover, state
  normalization table, pre-Run cache purity (zero backend calls),
  concurrent readers, cancel. `syncer_bench_test.go`: benchmarks at
  10/100/1000 torrents (full rebuild, 10-item delta, state read).
- **Unit tests** — next to the Go code (`cd omatorrent-service &&
  go test -race ./...`):
  - `internal/ipc/protocol_test.go` — grammar acceptance/rejection and
    exact response encodings.
  - `internal/ipc/server_test.go` — real-socket contract tests (below).
  - `internal/qbittorrent/client_test.go` — adapter against httptest
    fixtures (login success/bad-credentials/banned, bypass mode, SID
    expiry re-login, unauthorized, decode errors, unreachable).
  - `internal/state/syncer_test.go` — full/delta merge cycles, degraded
    states, recovery after failure, error classification, cancel.
  - `internal/config/config_test.go` — defaults, file load, permissive
    file refusal, explicit-missing error, env override.
- **Contract tests (IPC)** — `internal/ipc/server_test.go` against real
  Unix sockets: hello handshake, version_mismatch, handshake_required,
  malformed frames (bad JSON, duplicate keys, unknown fields, invalid
  UTF-8, trailing JSON, null), exact-4096 accepted / 4097 rejected,
  unsupported after handshake, clean disconnect (pre- and post-handshake),
  reconnect after orderly server restart, stale-socket recovery
  (proven-dead socket removed and reused; live daemon refused; wrong
  permissions/symlink/non-socket/ambiguous-hanging-listener refused
  untouched), client limit (17th closed), socket perms 0600, unsafe
  parent dirs refused, shutdown closes stalled clients +
  identity-checked socket removal, degraded system.status.
  Example frames in `contracts/ipc/v1/` are consumed by tests.
- **Incremental state** — `internal/state/syncer_test.go`: full/delta
  merge, partial-field semantics (absent vs explicit zero), removals,
  last-known-good on malformed payloads, rebuild-no-ghost, concurrent
  readers, cancel, state normalization.
- **Integration (live backend)** — Phase 0 evidence gathered manually +
  scripted: live probes (`/api/v2/app/version`, `webapiVersion`,
  `transfer/info`, `sync/maindata` deltas — read-only), daemon ↔ real
  qbittorrent-nox via `ot-probe` (systemd-run). Mutation semantics were
  live-verified in Phase 0.3 ONLY through a disposable random-infohash
  magnet (never resolvable, no data, removed with both deleteFiles
  variants) plus no-op unknown-hash calls; the user's real torrents are
  never mutated (count verified 3 → 3 around every run).
- **IPC v1.1 subscriptions** — snapshot frame sequence (name-sorted,
  count/index/id), delta push, delta chunking (bounded frames, shared
  seq), invalid subscribe schema, reconnect/resubscribe with fresh
  snapshot, name cap, slow-subscriber disconnect, v1.0 requests still
  served on a subscribed connection.
- **Mutations — adapter** (`internal/qbittorrent/client_mutations_test.go`):
  stop/start/pause/resume paths and form shapes, delete with EXPLICIT
  deleteFiles false/true, add with structured echo and legacy body,
  409 → ErrConflict, unexpected status, unreachable, 403-relogin retry.
- **Mutations — orchestration** (`internal/mutate/mutator_test.go`):
  stale/invalid/duplicate/unavailable validation without backend calls,
  version gating (≥ 2.11.0 stop/start, legacy pause/resume, garbage
  version), reconciliation confirmed/timeout/degraded-never-confirms,
  replay after completion never re-executes (incl. remove-with-files),
  in-flight replay same mutation id, ref_conflict, busy cap, echo
  mismatch rejection, ring bounds.
- **Mutations — IPC contract** (`internal/ipc/server_mutations_test.go`):
  exact accepted/rejected frames, delete_files boolean enforcement
  matrix (missing/string/number/null ⇒ invalid_message + close), schema
  matrix (hash/ref/url/key sets), replay result carries request id,
  result push reaches only mutating connections, disabled server,
  anti-drift example files.
- **Shell checks** — `omarchy plugin validate` (manifest schema, both
  plugins), `tools/test_quickshell.sh` (isolated `qs` instance speaking
  IPC v1 to the daemon with the widget's one-in-flight/id-matching
  discipline, including mismatched-id rejection; Phase 0.3: the full
  v1.2 mutation lifecycle against the disposable torrent —
  add/duplicate/replay/invalid/pause/resume/remove both variants/stale;
  Phase 0.4: v1.3 dashboard.status exact-key schema, aggregate
  invariants, counts.total cross-check against the v1.1 snapshot, and a
  deterministic dashboard-frame guard model), live bar + panel +
  dashboard observations + screenshots (visual-runtime standard),
  degraded states observed (daemon stop/start), journal error-free.
- **Dashboard lifecycle (0.4)** — `omarchy-shell shell toggle
  local.omatorrent-dashboard` open/close cycles (50×) leave daemon fd
  and socket-peer counts flat (destroy-on-close overlay), Escape and
  click-outside close, reopen after shell restart and after daemon
  restart recovery; theme light/dark observed via `omarchy theme set`.

- **Remote connection (0.5)** — `internal/connection`: URL validation
  matrix, profile store discipline (permissions/symlinks/atomicity/
  malformed/oversized, LoadActive fallback), the degraded-state matrix
  A–O against httptest fixtures (local normal; remote HTTPS via the
  full TOFU pin flow; DNS unreachable; TCP refused; auth_required;
  bad password incl. sticky frugality; session expiry re-auth success
  and failure; untrusted certificate; hostname mismatch; insecure-HTTP
  policy; malformed URLs; old WebAPI still syncs — no hard gate, by
  design; A→B backend switch incl. mutations_pending guard and rid
  reset; daemon restart with a remote profile), configure secret
  intents (keep/replace/delete, secrets_unavailable). Syncer switch/
  sticky suites in `internal/state/syncer_switch_test.go`; mutator
  epoch-guard suites in `internal/mutate/mutator_switch_test.go`;
  adapter remote suites (version-adaptive login both eras, redirects
  refused with observable targets, TLS system/CA/pin incl. mismatch
  and hostname classes with fingerprint extraction, 202 add, zeroed
  fetch-per-login, logout, protocol-mismatch classification,
  path-prefix joins) in `internal/qbittorrent/client_remote_test.go`;
  v1.4 wire schemas/anti-reflection/fixtures in
  `internal/ipc/server_connection_test.go`. Live evidence (disposable
  qbittorrent-nox fixture on the LAN IP with self-signed HTTPS, temp
  password, zero torrents, killed after): insecure_http refusal and
  acknowledged activation, real Secret Service store/lookup/clear,
  tls_untrusted with fingerprint cross-checked against openssl, pin
  trust flow, auth_required/auth_failed sticky ladder (3 attempts, no
  ban), certificate-rotation pin mismatch, restore-to-local — recorded
  in docs/agent/PHASE05.md. `tools/test_quickshell.sh` additionally
  runs four v1.4 connection stages (status key set, anonymous test
  against the live backend, idempotent configure with epoch advance,
  epoch verification) after the mutation stages.

## done_when examples (the standard)

Bad: "IPC is robust." Good: handshake contract test passes; incompatible
protocol version rejected safely (test output cited); reconnect after
service restart verified; malformed-message fuzz cases pass.

Bad: "UI is optimized." Good: no qBittorrent HTTP call exists in QML (grep
evidence); lists render incrementally; repeated open/close leaves no stale
state; memory/CPU observations recorded.

## CI [PROPOSAL]

GitHub Actions running build + vet + unit + contract suites on PRs; the
0.9 milestone adds release-candidate gates. No CI secrets needed for
local suites; live-backend jobs stay opt-in.
