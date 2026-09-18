# OmaTorrent — Testing & Evidence

Status: PHASE 0 — suites exist and were executed (see
docs/agent/PHASE0.md for the recorded run). Evidence rules used by
/ot-verify, the omatorrent-verification skill, and the qa-release agent.

## States (project vocabulary)

PASS / FAIL / NOT RUN / NOT AVAILABLE / BLOCKED — defined in the
omatorrent-verification skill. A requirement is complete only with recorded
evidence; "code looks right" is never sufficient.

## Suite layout (as built in Phase 0)

- **Unit tests** — next to the Go code (`cd omatorrent-service &&
  go test -race ./...`):
  - `internal/ipc/protocol_test.go` — grammar acceptance/rejection and
    exact response encodings.
  - `internal/ipc/server_test.go` — real-socket contract tests (below).
  - `internal/qbittorrent/client_test.go` — adapter against httptest
    fixtures (login success/bad-credentials/banned, bypass mode, SID
    expiry re-login, unauthorized, decode errors, unreachable).
  - `internal/state/manager_test.go` — snapshot cache, degraded states,
    recovery after failure, error classification, cancel.
  - `internal/config/config_test.go` — defaults, file load, permissive
    file refusal, explicit-missing error, env override.
- **Contract tests (IPC)** — `internal/ipc/server_test.go` against real
  Unix sockets: hello handshake, version_mismatch, handshake_required,
  malformed frames (bad JSON, duplicate keys, unknown fields, invalid
  UTF-8, trailing JSON, null), exact-4096 accepted / 4097 rejected,
  unsupported after handshake, clean disconnect, reconnect after orderly
  server restart, stale socket refusal, client limit (17th closed),
  socket perms 0600, unsafe parent dirs refused, shutdown closes stalled
  clients + identity-checked socket removal, degraded system.status.
  Example frames in `contracts/ipc/v1/` are consumed by tests.
- **Integration (live backend)** — Phase 0 evidence gathered manually +
  scripted: live probes (`/api/v2/app/version`, `webapiVersion`,
  `transfer/info`, `sync/maindata` deltas — read-only), daemon ↔ real
  qbittorrent-nox via `ot-probe` (systemd-run), no mutations.
- **Shell checks** — `omarchy plugin validate` (manifest schema),
  `tools/test_quickshell.sh` (isolated `qs` instance speaking IPC v1 to
  the daemon), live bar observations + screenshots (visual-runtime
  standard), degraded states observed (daemon stop/start), journal
  error-free.

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
