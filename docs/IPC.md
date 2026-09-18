# OmaTorrent — IPC Contract

Status: PLACEHOLDER — the contract is designed in milestone 0.1 via the
omatorrent-architecture workflow (ADR if boundaries change). This file will
hold the versioned protocol once designed. Nothing here is implemented.

## Decided constraints [DECISION]

- Transport: Unix-domain socket (ADR-0003), expected under XDG_RUNTIME_DIR
  (exact path OPEN).
- The protocol is explicitly versioned; the handshake is the first exchange;
  incompatible major versions are rejected safely.
- Malformed messages must never crash either side; message sizes bounded.
- QML never speaks this protocol directly with the daemon's business
  features bypassed — the IPC surface IS the only shell↔daemon boundary
  (ADR-0001).

## Required contract test cases (from docs/TESTING.md)

- handshake succeeds;
- incompatible protocol version rejected safely;
- reconnect works after service restart;
- malformed messages do not crash either process.

## Open questions

- [OPEN] Encoding: JSON-lines vs binary framing (decided at 0.1 design).
- [OPEN] Event push model for bar updates (server-push vs versioned poll).
- [OPEN] Confirmation-flag shape for destructive operations.
