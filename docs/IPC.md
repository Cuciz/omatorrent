# OmaTorrent — IPC Contract v1

Status: v1 (ADR-0004) + v1.1 extension (ADR-0005, read-only torrent
state) + v1.2 extension (ADR-0006, staged mutations). No secrets.
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

Protocol is an integer major version; only 1 is accepted. The v1.1
extension (ADR-0005) adds new message types only — it never adds fields
to existing shapes, so strict v1 clients are unaffected. A future
incompatible extension needs a reviewed compatibility plan rather than
adding fields to existing shapes.

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
- `dl_speed`/`up_speed`: integers, bytes/second; `torrents_total`:
  integer — all derived from the daemon's `sync/maindata` cache (0.2+;
  no per-poll torrent-list downloads).

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

## v1.1 extension: torrent state subscription (ADR-0005)

Read-only. Subscriptions deliver the daemon's normalized torrent state;
qBittorrent states, rids and merge semantics never cross this boundary.

### Subscribe (after hello; exact two keys, like health)

```json
{"type":"torrent.subscribe","id":3}
```
Response:
```json
{"type":"torrent.subscribed","protocol":1,"id":3}
```
Then, immediately, the full state as bounded frames (each ≤ 4096 bytes
incl. LF; ordering guaranteed per connection):
```json
{"type":"torrent.snapshot.begin","protocol":1,"id":3,"count":2}
{"type":"torrent.snapshot.item","protocol":1,"id":3,"index":0,"torrent":{"hash":"…40 or 64 hex…","name":"…","state":"seeding","progress":1,"dlspeed":0,"upspeed":51200,"eta":8640000,"ratio":2.1,"category":"","size":1048576,"completed":1048576}}
{"type":"torrent.snapshot.item","protocol":1,"id":3,"index":1,"torrent":{…}}
{"type":"torrent.snapshot.end","protocol":1,"id":3}
```
Thereafter, on each daemon state change:
```json
{"type":"torrent.delta","protocol":1,"seq":7,"changed":[{…torrent…}],"removed":["…hash…"]}
```
`changed`/`removed` are always JSON arrays (possibly empty, never
`null`). Large changes are split into multiple `torrent.delta` frames
sharing the same `seq`; clients apply each frame's `changed`/`removed`
as it arrives. `seq` is a daemon-global monotonically increasing value:
strictly increasing within a subscription, may start at any number and
contain gaps; it identifies ordering, not per-subscription counting.

### Normalized torrent item (exact key set; identical everywhere it appears)

| Key | Type | Meaning |
|---|---|---|
| hash | string | 40 or 64 hexadecimal characters (BitTorrent v1/v2 infohash) |
| name | string | capped at 512 UTF-8 runes by the daemon |
| state | string | downloading, seeding, paused, queued, checking, error, moving, other |
| progress | number | 0..1 |
| dlspeed / upspeed | integer | bytes/second |
| eta | integer | seconds; 8640000 = unknown/infinite |
| ratio | number | ≥ 0 |
| category | string | may be empty |
| size / completed | integer | bytes |

### Lifecycle and flow control

- One subscription per connection; a second `torrent.subscribe` gets an
  `unsupported_message` error frame but does NOT close the connection
  (the existing subscription keeps working). The subscription ends when
  the connection closes.
- Every (re)subscription starts with a fresh full snapshot under its own
  id; stale frames cannot survive a reconnect.
- Snapshot delivery is serialized with BACKPRESSURE: the server writes
  snapshot frames one by one, waiting for queue space, bounded by a
  total delivery window (30 s). A snapshot larger than the live queue
  (e.g. 1 000 torrents ≈ 1 003 frames) never disconnects a healthy
  subscriber; a subscriber that cannot drain the snapshot within the
  window is disconnected.
- Live deltas after the snapshot use a bounded per-connection queue
  (256 frames); queue overflow or an unwritable subscriber (5 s write
  deadline exceeded) closes the connection — the client re-handshakes,
  re-subscribes, rebuilds. A change committed during snapshot delivery
  is delivered strictly after `snapshot.end`.
- Failure behavior for pathological data: normalized items always fit
  the frame budget (hashes validated 40/64 hex, names ≤512 runes,
  categories ≤128 runes at daemon normalization). If a committed item
  nevertheless cannot be encoded within the budget, the daemon
  TERMINATES the subscriber connection — the client reconnects and
  rebuilds from a fresh snapshot. A committed update is never silently
  dropped or partially hidden.
- While subscribed, the v1.0 requests (health/system.status) remain
  available on the same connection.
- The daemon pushes only committed state: a snapshot or delta is never
  emitted mid-merge; a backend anomaly discards the partial cycle
  (last-known-good) so clients never see corrupt state.

## v1.2 extension: staged torrent mutations (ADR-0006)

The protocol's only backend-mutating surface. Core principle:
**request accepted ≠ mutation confirmed.** Acceptance means the daemon
validated the request against its committed state and the backend
acknowledged the submission; confirmation arrives as a separate,
state-derived result. Clients must never present optimistic state as
success. All qBittorrent vocabulary (stop/start vs pause/resume,
deleteFiles, 409 semantics) stays daemon-side; QML expresses intent only.

### Requests (after hello; exact key sets)

```json
{"type":"torrent.pause","id":4,"hash":"…40 or 64 hex…","ref":"r-…"}
{"type":"torrent.resume","id":5,"hash":"…","ref":"r-…"}
{"type":"torrent.add","id":6,"url":"magnet:?xt=urn:btih:…","ref":"r-…"}
{"type":"torrent.remove","id":7,"hash":"…","delete_files":false,"ref":"r-…"}
```

- `ref` — client-generated idempotency key, 1–128 chars of
  `[A-Za-z0-9._:-]`, unique per mutation ATTEMPT. Enables daemon-side
  replay handling (below). Never echoed in responses.
- `hash` — 40/64 hex at parse time; anything else is `invalid_message`.
- `delete_files` — REQUIRED JSON boolean, no default, on
  `torrent.remove` only. Missing/non-boolean ⇒ `invalid_message`
  (connection closes): destructive ambiguity is a protocol violation.
  The daemon forwards the intent explicitly to qBittorrent and never
  infers or upgrades delete-files intent.
- `url` — string ≤ 2048 bytes starting with `magnet:`; structural magnet
  validation happens daemon-side and rejects with `invalid_url` (a
  pasted non-magnet is a user mistake, not a protocol violation).
- One mutation in flight per connection by construction (handlers run
  in the connection's lockstep loop, bounded 5 s); a daemon-wide cap of
  4 concurrent submissions rejects excess with `busy`.

### Stage 1 — request response

Accepted (mutation is a daemon-global monotonic id; action echoes the
request type; hash is the target — for add, the daemon-parsed infohash
cross-checked against the backend echo on ≥ 5.2.0 backends):
```json
{"type":"mutation.accepted","protocol":1,"id":4,"mutation":12,"action":"torrent.pause","hash":"…"}
```
Rejected (no mutation performed or backend refused; connection stays
open; no payload echoed):
```json
{"type":"mutation.rejected","protocol":1,"id":4,"code":"stale_torrent"}
```

| Code | Condition |
|---|---|
| stale_torrent | hash not in the daemon's committed state (ghost row / already removed / wrong identity) |
| invalid_url | magnet failed daemon-side structural validation (scheme, `xt` urn, hex btih) |
| duplicate | add: hash already present in committed state |
| backend_rejected | backend refused (e.g. add 409 not explained by state, other non-2xx) |
| backend_unavailable | backend down (sync degraded) or submission transport failure |
| busy | daemon-wide in-flight cap exceeded |
| ref_conflict | ref replayed with different action/parameters than recorded |

### Stage 2 — terminal result (push, ≤ 10 s reconcile window)

```json
{"type":"mutation.result","protocol":1,"mutation":12,"action":"torrent.pause","hash":"…","status":"confirmed"}
```
`confirmed` = committed sync state observed the intent (pause ⇒ state
`paused`; resume ⇒ present and not `paused`; add ⇒ present; remove ⇒
absent). `timeout` = window elapsed without confirmation: the outcome is
AMBIGUOUS and surfaced as such; committed state remains the only
authority. Results are delivered exactly once per mutation, on every
connection that has issued at least one mutation request (pure status
clients such as the bar widget never receive them).

A REPLAYED ref is answered as the request response in one terminal
frame: the recorded `mutation.rejected` code, or a `mutation.result`
carrying the replaying request's `id` plus the recorded terminal status
— a second wire shape for the same type:
```json
{"type":"mutation.result","protocol":1,"id":8,"mutation":12,"action":"torrent.pause","hash":"…","status":"confirmed"}
```

### Replay and retry rules

- In-flight `ref` replay ⇒ `mutation.accepted` with the SAME mutation id
  (no second backend call).
- Completed `ref` replay ⇒ one terminal frame with the recorded outcome
  (rejected code or result status). No second backend call — a same-hash
  remove can never execute twice through a retry.
- Deduplication is per-daemon-process (NOT durable across daemon
  restarts): clients must never auto-replay destructive refs across a
  daemon restart; a fresh user action is required. pause/resume are
  naturally idempotent and MAY be re-issued with a new ref; add is
  duplicate-protected on modern backends; remove is NEVER retried
  blindly — re-derive from state, then require fresh confirmation.

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
| unsupported_message | After hello, a valid message type other than health/system.status/torrent.subscribe/torrent.pause/torrent.resume/torrent.add/torrent.remove, including another hello; also a second torrent.subscribe on an already-subscribed connection (error only, connection stays open) |

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
produced from cached state, so they arrive promptly — the one exception
is the v1.2 mutation stage-1 response, which performs one bounded
(≤ 5 s) backend submission inline under the lockstep discipline; the
degraded `qbittorrent:"unavailable"` shape is the answer whenever the
cache holds no live backend state (startup window or backend down).
Every client is closed on shutdown, including clients stalled halfway
through a frame.

## Client obligations (Phase 0 QML client)

- Connect, handshake, then poll `system.status` at a slow cadence
  (2 s in the proof) and/or `health` on demand.
- Keep **at most one request in flight** per connection; match responses
  by `id` and ignore responses whose id is not the pending one.
- On disconnect or error response: drop state including the pending id,
  render the offline state, reconnect with bounded backoff, and handshake
  again before further requests.
- Mutations (v1.2): generate a fresh `ref` per attempt; treat
  `mutation.accepted` as "submitted", never as success; clear all
  pending overlays on disconnect and re-derive from the fresh snapshot;
  never auto-retry `torrent.remove` (fresh user confirmation required);
  render `timeout` results as ambiguous, not failed.
- Never send qBittorrent data, credentials, or derived secrets; this
  protocol carries none.

## Verification

See docs/DEVELOPMENT.md for executable checks. Contract tests use real Unix
sockets, including exact/oversize frames, UTF-8/duplicate-key rejection,
permissions, collision/replaced-path protection, client bounds/timeouts,
shutdown and reconnect, and the `system.status` ok/unavailable paths.
Example frames live in contracts/ipc/v1/ and are consumed by tests so
examples cannot silently drift.
