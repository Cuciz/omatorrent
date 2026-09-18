# ADR-0004: Minimal Phase 0 JSON Lines IPC v1

STATUS: ACCEPTED (2026-09-18; amended same day before first commit — the
original WIP scoped the plugin as inert with hello/health only; the Phase 0
goal was widened to a live end-to-end proof, so the contract gained request
ids, backend-aware health and system.status. Framing, socket lifecycle and
error model are unchanged from the reviewed draft.)

## CONTEXT

ADRs 0001–0003 fix presentation-only QML, Go and a versioned Unix socket.
The user requests a runnable Phase 0 end-to-end proof — bar widget → socket
→ daemon → qBittorrent WebAPI — with real state and truthful offline states,
still without product features. Framing and socket lifecycle must be
concrete before implementation. There are no existing consumers to migrate.

## DECISION

Use bounded UTF-8 JSON Lines (NDJSON), protocol major 1, with hello,
health and system.status. The exact normative message contract, errors and
limits are in docs/IPC.md.

- NDJSON over JSON-RPC: the protocol is strict lockstep request→response
  with an integer request id echoed in responses; JSON-RPC's method
  envelope and error object add grammar without adding capability, while
  LF framing maps directly to Quickshell's `SplitParser`. Binary framing
  was already rejected for complexity without need.
- `health` reports IPC readiness plus last-observed qBittorrent
  reachability (`ok`/`unavailable`) and nothing more.
- `system.status` returns a small flat snapshot: qBittorrent app/WebAPI
  versions, global down/up speeds, torrent count; a degraded 4-key shape
  when qBittorrent is unreachable. It is served from a short-lived daemon
  cache; clients poll no faster than 1 Hz.
- `torrent.snapshot` and all mutations are deferred: the 0.2 incremental
  sync (`sync/maindata` + rid) must shape the torrent-list schema, and
  mutations need confirmation-flow design. Adding them requires a
  reviewed v1.x extension, not silent field additions.
- Reject duplicate keys, unknown fields, invalid UTF-8 and malformed or
  oversized input. Error responses never echo payload or ids. Limit
  connections and handshake/idle deadlines.

Socket lifecycle (unchanged): create $XDG_RUNTIME_DIR/omatorrent/service.sock
under an existing absolute, owned, private runtime directory (0700); the
application subdirectory is 0700 and socket 0600. Reject symlinks in the
runtime path and unsafe existing directories, never repair permissions
automatically. Refuse any existing socket or file, including a stale
socket. Disable Go automatic socket unlink; shutdown removes the socket
only when its file identity matches the one created by this process.
Processes with the same UID remain inside the trust boundary: a same-user
attacker can race path checks or modify private files. A stale socket after
an unclean exit requires deliberate operator cleanup after checking the
service is stopped; no automatic replacement is provided.

The Phase 0 QML client is no longer inert: it connects via Quickshell.Io
`Socket` (QLocalSocket → Unix domain on Linux) with a `SplitParser`
(newline framing) — verified present in installed Quickshell 0.3.1 — and
implements handshake, 2 s status polling, offline rendering and bounded
reconnect backoff. It performs no HTTP and holds no secrets (ADR-0001
holds).

Use development plugin ID local.omatorrent. Public marketplace identity is
still OPEN. Use a monorepo with one Go module (omatorrent-service/), native
plugin (plugins/local.omatorrent/), contracts/ipc/v1 examples and shared
tools.

## CONSEQUENCES

The end-to-end architecture is provable with real qBittorrent state while
the contract stays small and strictly testable. No external Go
dependencies, broker, TCP listener, storage or background sync loops are
introduced. Same-major extensions and minor negotiation remain future
work; strict v1 consumers must not receive new fields silently. The
production state subscription (push or rid-leasing) and any mutations
require a subsequent contract design/review.

## ALTERNATIVES

- JSON-RPC 2.0 over the same framing: rejected — redundant envelope for a
  lockstep protocol; would complicate the strict duplicate/unknown-field
  rejection rules.
- Binary framing: rejected earlier, unchanged — complexity without need.
- HTTP/TCP and direct QML networking contradict ADR-0003/0001.
- Automatically deleting pre-existing sockets risks disrupting another
  instance and is rejected.

## EVIDENCE/SOURCES

- docs/agent/PHASE0.md: bounded plan, independently reviewed before code.
- Installed Quickshell 0.3.1 and Omarchy plugin manifest schema 1;
  Quickshell.Io `Socket`/`SplitParser` types verified in
  /usr/lib/qt6/qml/Quickshell/Io/quickshell-io.qmltypes (Socket: path,
  connected, write; DataStream.parser).
- Live qBittorrent probes 2026-09-18: app/version v5.2.3,
  app/webapiVersion 2.15.1, transfer/info, torrents/info (docs/QBITTORRENT.md).
- Go net.UnixListener.SetUnlinkOnClose documentation:
  https://pkg.go.dev/net#UnixListener.SetUnlinkOnClose
- JSON decoder caveats (duplicates and invalid UTF-8):
  https://pkg.go.dev/encoding/json#hdr-Security_Considerations
