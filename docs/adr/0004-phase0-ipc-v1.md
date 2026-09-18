# ADR-0004: Minimal Phase 0 JSON Lines IPC v1

STATUS: ACCEPTED (2026-09-18; amended twice before merge:
amendment 1 widened the original inert-plugin draft to the live
end-to-end proof — request ids, backend-aware health, system.status;
amendment 2, from PR review, (a) replaced operator-only stale-socket
cleanup with fail-closed proven-stale recovery, (b) made status/health
strictly cache-served, and (c) made one-request-in-flight + id matching
normative for clients. Framing and error model are unchanged.)

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

Socket lifecycle (amendment 2): create $XDG_RUNTIME_DIR/omatorrent/
service.sock under an existing absolute, owned, private runtime directory
(0700); the application subdirectory is 0700 and socket 0600. Reject
symlinks in the runtime path and unsafe existing directories, never
repair permissions automatically. Disable Go automatic socket unlink;
shutdown removes the socket only when its file identity matches the one
created by this process. An existing socket path is refused — with one
exception added by amendment 2: a socket of the exact expected shape
(type socket, no symlink, UID-owned, 0600) whose listener is proven dead
by an ECONNREFUSED connect probe is removed (identity re-checked between
probe and unlink) and the path reused. A full IPC v1 hello against the
socket that receives the exact expected response means a live daemon
owns it and startup is refused; connect timeouts or unexpected answers
are ambiguous and fail closed. Nothing is deleted blindly. Processes
with the same UID remain inside the trust boundary: a same-user attacker
can race path checks or modify private files.

Status serving (amendment 2): health and system.status are answered
exclusively from the background refresher's cache; no IPC request ever
contacts qBittorrent synchronously. Before the first refresh completes,
the degraded shapes are returned. Clients must keep at most one request
in flight and match responses by id (normative in docs/IPC.md).

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
