# ADR-0005: IPC v1.1 — read-only torrent state delivery via subscription

STATUS: ACCEPTED (2026-09-18, Phase 0.2 design; ADR-level per AGENTS.md —
extends the v1 message table without touching existing shapes).

## CONTEXT

Phase 0.2 adds a panel that must render live torrent state. Polling a
`torrent.snapshot` response would be O(N) per poll and cannot fit the
4096-byte frame budget for large lists (1 000 torrents ≈ 200 KB). The
daemon already learns changes incrementally from `sync/maindata`
(docs/QBITTORRENT.md); the contract needs a delivery mechanism that
carries a full snapshot once and small deltas afterwards, without
modifying any strict v1 response shape and without mutations.

## DECISION

Extend IPC v1 to **v1.1** with one new request type and four new
server-initiated frame types. No existing request/response shape
changes; v1 clients that never send the new request see no difference
(strict-compatibility rule from ADR-0004 holds).

### Messages

- Request `{"type":"torrent.subscribe","id":N}` (exact two keys, like
  health/system.status). Response `{"type":"torrent.subscribed","protocol":1,"id":N}`.
- Immediately after, the server delivers the full state as bounded
  frames, each ≤ 4096 bytes including LF:
  - `{"type":"torrent.snapshot.begin","protocol":1,"id":N,"count":M}`
  - `{"type":"torrent.snapshot.item","protocol":1,"id":N,"index":i,"torrent":{...}}`
    (one per torrent; `torrent` carries the normalized item schema below)
  - `{"type":"torrent.snapshot.end","protocol":1,"id":N}`
- Thereafter, on each daemon state change, the server pushes
  `{"type":"torrent.delta","protocol":1,"seq":S,"changed":[torrent...],"removed":[hash...]}`
  frames, split into as many frames as the byte budget requires (all
  frames of one change share `seq`; clients apply frames as they
  arrive; ordering per connection is guaranteed). `seq` is the daemon's
  global change counter: strictly increasing within a subscription,
  possibly with gaps — ordering identity, not a per-subscription index.
  Implementation hardening from review: backend hashes are validated
  (40/64 hex) and categories capped (128 runes) at normalization, so
  uncapped backend strings cannot breach the frame budget; snapshot
  items are size-guarded at encode time as defense in depth.
- Subscriptions end when the connection closes. The v1.0 request set
  (hello/health/system.status) remains available on the same
  connection.

### Normalized torrent item schema (identical in snapshot.item and delta)

```json
{"hash":"…40 hex…","name":"…","state":"downloading","progress":0.42,
 "dlspeed":0,"upspeed":0,"eta":86400,"ratio":1.5,"category":"…",
 "size":0,"completed":0}
```

- `state` is the daemon's normalized set: `downloading`, `seeding`,
  `paused`, `queued`, `checking`, `error`, `moving`, `other` — mapped
  from qBittorrent states INSIDE the daemon (QML never sees qBittorrent
  state strings). Unknown qBittorrent states map to `other`.
- `eta` seconds (8640000 sentinel for ∞); `progress` 0..1; numbers are
  integers except progress/ratio.
- `name` is capped at 512 UTF-8 runes (documented truncation for the
  frame budget); `hash` is 40 hex chars.
- No qBittorrent-version, path, tracker, or secret data crosses the IPC.

### Delivery and flow control

- One subscription per connection; at most 16 connections total
  (unchanged).
- Per-subscriber bounded queue (256 frames). Overflow or a blocked
  writer (5 s write deadline exceeded) closes the connection — the
  client re-handshakes, re-subscribes and rebuilds from the fresh full
  snapshot. Slow consumers cannot grow daemon memory.
- Reconnect semantics: every new subscription starts with a full
  snapshot under a fresh id; `seq` restarts per subscription and is
  strictly increasing within one. Stale frames cannot survive a
  reconnect because the connection (and its id/seq space) died first.
- Snapshot/delta frames are emitted from the daemon's committed state
  only — never a half-merged sync cycle.

## CONSEQUENCES

- The panel gets O(changed) updates after an O(N) initial snapshot.
- The server gains push writes; the strict v1 grammar for existing
  types, deadlines, error model and socket lifecycle are unchanged.
- `system.status` continues to exist for the bar widget; the daemon
  derives its torrents count/speeds from the same sync state cache
  (torrents/info polling is removed).
- Versioning: the hello exchange still reports protocol 1; v1.1 is a
  compatible extension. A future incompatible change bumps to 2.

## ALTERNATIVES

- Polling `torrent.snapshot` with pagination: O(N) bytes per poll,
  client-driven paging complexity, stale-window artifacts — rejected.
- Raising the frame limit to 256 KB: breaks the bounded-memory and
  anti-abuse posture of v1 everywhere — rejected.
- WebSocket/gRPC/protobuf: rejected (ADR-0003/0004 rationale; no need
  demonstrated for a local single-user socket).
- Client-side rid proxying (exposing qBittorrent rid semantics over
  IPC): leaks backend implementation details into QML — rejected
  (ADR-0001).

## EVIDENCE/SOURCES

- docs/QBITTORRENT.md — live-confirmed sync/maindata semantics
  (partial-field deltas, session-scoped rid, full_update triggers).
- docs/IPC.md v1.1 section (normative text).
- Frame budget arithmetic: measured normalized item ≈ 180–260 bytes;
  1 000-item snapshot ≈ 250 KB ⇒ chunking is mandatory at 4 096 bytes.
