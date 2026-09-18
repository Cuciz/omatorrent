# ADR-0003: Shell↔daemon communication uses a versioned Unix-domain IPC protocol

STATUS: ACCEPTED (2026-09-18, project bootstrap)

## CONTEXT

ADR-0001 splits presentation (Quickshell) from business logic (daemon) on
one machine. The two processes must exchange state and user intents with:
no network exposure, clear local trust boundaries, safe version skew during
updates (plugin and daemon update independently), and robustness against
malformed messages. No third process or broker is desired.

## DECISION

Communication uses a Unix-domain socket with an explicitly versioned IPC
protocol: the first exchange is a version handshake; compatible versions
proceed, incompatible major versions are rejected safely; message sizes are
bounded and malformed messages never crash either side.

## CONSEQUENCES

- Local-only by construction; permissions on the socket file define who may
  connect (must be set explicitly — see docs/SECURITY.md).
- Version skew becomes an explicit, testable state instead of undefined
  behavior.
- Contract tests (handshake / version-reject / reconnect / malformed-input)
  are the boundary's done-when evidence (docs/TESTING.md).
- Encoding (JSON-lines vs binary framing) remains OPEN — decided at 0.1
  contract design; this ADR fixes transport and versioning, not encoding.

## ALTERNATIVES

- HTTP on a localhost TCP port: rejected — network-exposed by default,
  port conflicts, weaker default trust boundary.
- D-Bus: considered — mature on Linux, but heavier contract machinery and
  weaker fit for versioned incremental state sync; revisit only with
  concrete evidence of need.
- Direct shared SQLite from QML: rejected — couples schema to UI, invites
  secrets into storage, no clean degradation story.

## EVIDENCE/SOURCES

- Architecture constraints in docs/ARCHITECTURE.md and docs/SECURITY.md.
- No implementation yet; decided at bootstrap before the 0.1 contract work.
