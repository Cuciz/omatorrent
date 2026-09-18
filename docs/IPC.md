# OmaTorrent — IPC Contract v1

Status: Phase 0 contract, ADR-0004. Read-only; no mutations, no secrets.
Encoding: newline-delimited JSON (NDJSON) — see ADR-0004 for the choice
vs JSON-RPC/binary framing.

## Transport and framing

Unix stream socket: $XDG_RUNTIME_DIR/omatorrent/service.sock. Runtime directory
must exist, be absolute, owned by the process UID, mode 0700, with no symlink
path components. Application directory 0700; socket 0600. Existing unsafe
directories cause startup failure; no permission repair.

An existing socket path is handled fail-closed with one narrow exception:
a socket file of the exact expected shape (type socket, no symlink,
UID-owned, mode 0600) whose listener is **proven dead** (connect returns
ECONNREFUSED) is removed and the path reused — this recovers the socket a
SIGKILL'd daemon left behind. The removal is guarded by a file-identity
re-check between probe and unlink. In every other case the daemon refuses
to start: another omatorrent-service answering an IPC v1 hello on the
socket (live instance), a connect that times out or answers anything other
than the exact hello response (ambiguous), wrong type/owner/permissions,
or a symlink. Nothing is ever deleted blindly; residual same-UID races
remain inside the documented trust boundary. Graceful shutdown closes
clients and removes only the original socket (file identity check).

A frame is one valid UTF-8 JSON object followed by LF, at most **4096 bytes
including LF**. JSON whitespace is allowed; no unknown fields, duplicate
keys, trailing JSON, arrays/null as messages or invalid field types. Keys
are case-sensitive. No payload content is logged or reflected in errors.

Protocol is an integer major version; only 1 is accepted. No minor version
negotiation exists yet. A future extension needs a reviewed compatibility
plan rather than adding fields to this strict grammar.

## Messages

### Handshake (always first)

```json
{"type":"hello","protocol":1}
```
Response (only these three keys):
```json
{"type":"hello","protocol":1,"service":"omatorrent-service"}
```

### Post-handshake requests

Every post-handshake request carries an integer `id` (client-assigned,
echoed verbatim in the matching response). Phase 0 clients use one request
in flight per connection; the lockstep request→response model makes `id`
matching unambiguous.

Health (only these two keys):
```json
{"type":"health","id":1}
```
Response:
```json
{"type":"health","protocol":1,"id":1,"service":"ready","backend":"ok"}
```
`backend` is `"ok"` or `"unavailable"`. `ready` means the IPC service is
responding; `backend` reports the daemon's last-observed qBittorrent
reachability. No further detail is exposed.

System status (only these two keys):
```json
{"type":"system.status","id":2}
```
Response when qBittorrent data is available (exact key set):
```json
{"type":"system.status","protocol":1,"id":2,"qbittorrent":"ok",
 "app_version":"v5.2.3","webapi_version":"2.15.1",
 "dl_speed":0,"up_speed":0,"torrents_total":3}
```
- `app_version`/`webapi_version`: strings from the live qBittorrent probes.
- `dl_speed`/`up_speed`: integers, bytes/second (from `transfer/info`).
- `torrents_total`: integer (torrent list length).

Response when qBittorrent is unreachable (only these four keys):
```json
{"type":"system.status","protocol":1,"id":2,"qbittorrent":"unavailable"}
```
`system.status` is the Phase 0 bar-widget operation. It is served
**exclusively from the daemon's state cache** — answering it never
contacts qBittorrent. All backend I/O happens in the daemon's background
refresher (2 s cadence, exponential backoff on failure); until the first
refresh completes after daemon startup, `system.status` and `health`
answer the degraded/unavailable shapes. Clients should not poll faster
than 1 Hz. `torrent.snapshot` (full torrent list) is deliberately
deferred to 0.2 with the incremental-sync design; adding it requires a
reviewed v1.x extension.

## Errors and resource limits

Response shape (then connection closes):
```json
{"type":"error","protocol":1,"code":"invalid_message"}
```

| Code | Condition |
|---|---|
| invalid_message | Invalid JSON/UTF-8/schema, duplicate/unknown fields, missing type/protocol/id, incomplete frame on EOF |
| message_too_large | Frame cannot fit in 4096 bytes including LF |
| handshake_required | A valid message other than hello arrives first |
| version_mismatch | First hello has an integer protocol other than 1 |
| unsupported_message | After hello, a valid message type other than health/system.status, including another hello |

Unknown message types use the type-only shape; adding other fields is
invalid_message. A hello never carries an id; health/system.status always
require an integer id. `id` must be a JSON integer (not float/string).
Error responses never echo `id` or any payload content (anti-reflection).
Parse/schema errors take precedence over handshake/state errors. Socket
I/O failure or timeout closes the connection without a guaranteed error
response.

At most 16 active clients; excess connections close without a response.
Handshake deadline: 5 seconds. Following frames: 30-second idle read
deadline; responses have a 5-second write deadline. Responses are always
produced from cached state, so they arrive promptly; the degraded
`qbittorrent:"unavailable"` shape is the answer whenever the cache holds
no live backend state (startup window or backend down). Every client is
closed on shutdown, including clients stalled halfway through a frame.

## Client obligations (Phase 0 QML client)

- Connect, handshake, then poll `system.status` at a slow cadence
  (2 s in the proof) and/or `health` on demand.
- Keep **at most one request in flight** per connection; match responses
  by `id` and ignore responses whose id is not the pending one.
- On disconnect or error response: drop state including the pending id,
  render the offline state, reconnect with bounded backoff, and handshake
  again before further requests.
- Never send qBittorrent data, credentials, or derived secrets; this
  protocol carries none.

## Verification

See docs/DEVELOPMENT.md for executable checks. Contract tests use real Unix
sockets, including exact/oversize frames, UTF-8/duplicate-key rejection,
permissions, collision/replaced-path protection, client bounds/timeouts,
shutdown and reconnect, and the `system.status` ok/unavailable paths.
Example frames live in contracts/ipc/v1/ and are consumed by tests so
examples cannot silently drift.
