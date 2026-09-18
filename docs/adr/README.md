# Architecture Decision Records

Lightweight ADRs for OmaTorrent. Each records one binding architectural
decision; implementation must conform until an ADR explicitly supersedes it.

## Index

| ADR | Decision | Status |
|---|---|---|
| [0001](0001-quickshell-daemon-boundary.md) | Quickshell is presentation only; business logic lives in a separate daemon | ACCEPTED |
| [0002](0002-go-service.md) | `omatorrent-service` is implemented in Go | ACCEPTED |
| [0003](0003-unix-socket-ipc.md) | Shell↔daemon communication uses a versioned Unix-domain IPC protocol | ACCEPTED |

## Format

Every ADR contains: STATUS (PROPOSED / ACCEPTED / SUPERSEDED by N) ·
CONTEXT · DECISION · CONSEQUENCES · ALTERNATIVES · EVIDENCE/SOURCES.

Rules:

- A change to an ADR-level decision requires a new (or superseding) ADR
  BEFORE implementation — never a silent change.
- Speculative choices are labeled as such inside the ADR; they are not
  universally proven facts.
- New ADRs take the next number; never reuse or renumber.
