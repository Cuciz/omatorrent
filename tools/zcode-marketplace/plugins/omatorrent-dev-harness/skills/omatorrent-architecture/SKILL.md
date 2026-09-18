---
name: omatorrent-architecture
description: Use when modifying OmaTorrent component boundaries, the Quickshell-daemon IPC contract, storage schema, backend abstraction, major dependencies, or when doing architectural refactoring — and when deciding whether a change needs an ADR. Enforces the ADR-first workflow and the QML-presentation-only / daemon-owns-logic invariants.
---

# OmaTorrent Architecture Workflow

Applies to any change that alters how OmaTorrent's components relate:
boundaries, IPC contract, storage, backend abstraction, dependencies,
refactoring. Everything else is an ordinary implementation task.

## Workflow — in order, no skipping

1. **Inspect.** Read docs/ARCHITECTURE.md, docs/IPC.md, and the existing
   ADRs (docs/adr/). Inspect the current code that the change touches.
2. **Find the existing ADR.** If a recorded decision covers this area, the
   change either conforms to it or must supersede it explicitly — never
   contradict it silently.
3. **Research.** Version-sensitive platform behavior (Omarchy Quattro plugin
   API, Quickshell, qBittorrent WebAPI) is verified against current official
   sources, not memory.
4. **Identify affected contracts.** List every interface the change breaks
   or extends: IPC messages, adapter interface, storage schema, QML surface.
5. **Propose.** Write the proposal: options considered, recommendation,
   migration path for existing consumers.
6. **Document the decision** as an ADR (next number in docs/adr/), states
   ACCEPTED or PROPOSED. An ADR is required before implementation when any
   invariant or recorded decision changes.
7. **Implement only after the consistency check.** The omatorrent-architect
   agent reviews the ADR + proposal against the codebase first.

## Invariants (ADR-0001..0003 and AGENTS.md)

- QML/Quickshell is presentation only; omatorrent-service owns all business
  logic; they talk over the versioned Unix-domain IPC protocol.
- qBittorrent-specific code stays behind the daemon's adapter boundary.
- Secrets never enter QML, logs, or SQLite.

## Output

End with: ADR needed (yes/no + number), affected contracts list, and the
consistency-check verdict.
