# OmaTorrent — Testing & Evidence

Status: DRAFT — the evidence rules used by /ot-verify, the
omatorrent-verification skill, and the qa-release agent. Suites do not exist
yet; this file defines their shape so they are built with the code.

## States (project vocabulary)

PASS / FAIL / NOT RUN / NOT AVAILABLE / BLOCKED — defined in the
omatorrent-verification skill. A requirement is complete only with recorded
evidence; "code looks right" is never sufficient.

## Suite layout [PROPOSAL — fixed at Phase 0/1]

- **Unit tests** — next to the Go code (`go test ./...`).
- **Contract tests** — the two real boundaries:
  - IPC: client/server contract against the versioned protocol (handshake
    succeeds; incompatible version rejected; malformed messages don't crash
    either side; reconnect after service restart).
  - Adapter: qBittorrent responses as recorded fixtures (each fixture
    labeled with qBittorrent + WebAPI version), exercised without a live
    backend.
- **Integration tests** — daemon ↔ real local qbittorrent-nox (capability
  probe, sync/maindata incremental updates, mutations).
- **Lifecycle/UI checks** — shell plugin load, panel open/close repetition
  without stale state, theme switch, daemon-unreachable degraded state;
  observed in the running shell (visual-runtime standards).

## done_when examples (the standard)

Bad: "IPC is robust." Good: handshake contract test passes; incompatible
protocol version rejected safely (test output cited); reconnect after
service restart verified; malformed-message fuzz cases pass.

Bad: "UI is optimized." Good: no qBittorrent HTTP call exists in QML (grep
evidence); lists render incrementally; repeated open/close leaves no stale
state; memory/CPU observations recorded.

## CI [PROPOSAL]

GitHub Actions running build + vet + unit + contract suites on PRs; the
0.9 milestone adds release-candidate gates. No CI secrets needed for local
suites; live-backend jobs stay opt-in.
