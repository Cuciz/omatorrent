# Sprout — IPC Contract v1

Status: v1 (ADR-0004) + v1.1 extension (ADR-0005, read-only torrent
state) + v1.2 extension (ADR-0006, staged mutations) + v1.3 extension
(ADR-0007, dashboard aggregates) + v1.4 extension (ADR-0008, connection
management). No secrets in any response.
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
authority.

**Delivery guarantee (accurate):** terminal results are pushed at most
once per live delivery subscription, on every connection that has
issued at least one mutation request (pure status clients such as the
bar widget never receive them). The daemon's delivery pump may briefly
resubscribe (bounded delay) — a result published in that gap is NOT
replayed onto existing connections, so **clients must not depend on
receiving a push.** Clients resolve pending mutations through the state
stream itself, and MAY resolve them with a same-ref watchdog: after a
bounded window, re-send the SAME `ref` with identical parameters — the
daemon answers from its recorded outcome (no backend execution; the
record is the per-process 64-slot ring, so the replay-rules limits
below apply).
`torrent.remove` is NEVER automatically re-sent (fresh confirmation
required); its pending overlay escalates to the ambiguous state and the
row settles via the snapshot/deltas. A client must never automatically
retry any mutation with a fresh `ref`.

**Frame order:** either legal order — `mutation.accepted` then
`mutation.result`, or `mutation.result` then `mutation.accepted` — can
occur on one connection (a result can be published while the request is
still being answered). Clients must treat responses and pushes as
independent, id-keyed/correlation-id-keyed streams; the reference panel
buffers unknown-id results in a bounded early-result map and applies
them when the matching acceptance arrives (plugins/local.omatorrent/
MutationClient.js — covered by deterministic ordering tests in
tools/test_quickshell.sh).

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

## v1.3 extension: dashboard aggregates (ADR-0007)

Read-only, current state only (no history — that is Phase 0.8). One
request type, served exclusively from the daemon's committed state
(answering never contacts qBittorrent; aggregation over the committed
torrent map is O(N + A log A) — N torrents plus a sort of the A active
candidates — with a measured full-response cost of a few milliseconds
at N = 1 000).

### Request (after hello; exact two keys, like health)

```json
{"type":"dashboard.status","id":9}
```

### Response — live (exact key set; `free_space` present iff the
backend has ever reported it — omitted means unknown, `0` is a real
value):

```json
{"type":"dashboard.status","protocol":1,"id":9,"qbittorrent":"ok",
 "app_version":"v5.2.3","webapi_version":"2.15.1",
 "dl_speed":1048576,"up_speed":131072,"free_space":50210201600,
 "counts":{"total":3,"active":1,"downloading":1,"seeding":1,"paused":1,"completed":2},
 "aggregate":{"total_size":3221225472,"completed_bytes":2147483648,"remaining_bytes":1073741824},
 "active":[{"name":"A live torrent with a long descriptive name","state":"downloading","progress":0.5,"dlspeed":1048576,"upspeed":4096}]}
```

Field semantics (definitions live daemon-side; the dashboard is a pure
renderer — ADR-0007):

| Field | Meaning |
|---|---|
| `counts.total` | torrents in committed state |
| `counts.active` | `dlspeed > 0 || upspeed > 0` (current values) |
| `counts.downloading`/`seeding`/`paused` | normalized-state equality; `queued`/`checking`/`error`/`moving`/`other` count only toward `total` — counts do NOT sum to `total` |
| `counts.completed` | `progress >= 1` (same predicate as the panel's Completed filter) |
| `aggregate.total_size`/`completed_bytes` | saturating sums of per-torrent `size`/`completed` (bytes) |
| `aggregate.remaining_bytes` | saturating sum of per-torrent `max(0, size − completed)` |
| `active[]` | the transferring-now list: active torrents, at most 5, combined speed descending, name ascending; current state only, never history; names capped at 48 runes (halved, then trailing entries dropped, if pathological names would exceed the frame budget — `counts.active` always reports the true count) |
| `free_space` | qBittorrent `server_state.free_space_on_disk` — free space on the disk of the default save path (WebAPI ≥ 2.1.1) |

### Response — degraded (qBittorrent unreachable)

```json
{"type":"dashboard.status","protocol":1,"id":9,"qbittorrent":"unavailable",
 "last_known":{"app_version":"v5.2.3","webapi_version":"2.15.1",
  "counts":{"total":3,"active":0,"downloading":0,"seeding":1,"paused":2,"completed":1},
  "aggregate":{"total_size":3221225472,"completed_bytes":1073741824,"remaining_bytes":2147483648}}}
```

`last_known` is present iff at least one sync cycle ever committed;
before the first sync the degraded response is the bare shape
(`{"type","protocol","id","qbittorrent"}` only). Speeds, `free_space`
and `active` are NEVER included while degraded — the daemon does not
know them and fake zeroes are forbidden. `last_known` values are
last-known-good and must be presented as such, not as live data.

Client rules: same lockstep discipline as v1.0 (one request in flight,
id-matched); poll cadence should not exceed 1 Hz; available on the same
connection alongside v1.0 requests and v1.1 subscriptions. Contract
fixtures: `contracts/ipc/v1/dashboard-status.txt`,
`response-dashboard-status.txt`,
`response-dashboard-status-degraded.txt`,
`response-dashboard-status-never-synced.txt`.

## v1.4 extension: connection management (ADR-0008)

Read-mostly surface for the connection settings UI. Profile metadata and
live connection status are cache-served; `connection.test` is the second
(and last) IPC message that performs bounded inline network work (≤ 8 s,
temporary client, no state change — the precedent is the v1.2 stage-1
mutation submission); `connection.configure` is the only message that
switches the backend epoch. No response ever contains a secret; the
optional request password crosses client→daemon once per user action
inside the 0600-socket trust boundary (ADR-0004/0008) and is never
echoed, logged or persisted.

### connection.status (exact two keys, like health)

```json
{"type":"connection.status","id":10}
```
Response (exact key set):
```json
{"type":"connection.status","protocol":1,"id":10,"configured":true,
 "mode":"local","url":"http://127.0.0.1:8080","host":"127.0.0.1:8080","transport":"http","insecure":false,
 "username":"","has_secret":false,"tls_mode":"system",
 "status":"connected","detail":"","epoch":0}
```
(`pin` — 64 hex — is additionally present iff `tls_mode` is `pin`;
`epoch` starts at 0 on daemon start and increments on every
configured switch. `connection.status` always carries `detail`
(possibly empty); the `connection.test` response omits `detail` when
empty.)
- `mode` — `local` (loopback URL) or `remote` (derived, never stored);
  `url` — the validated origin (non-secret; the settings form prefills
  it — like `username`, status surfaces display only `host`).
- `host` — display-safe label `host[:port][/path]`, capped at 128 runes;
  never a userinfo, secret or full URL echo.
- `transport` — `http` | `https`; `insecure` — the FACTUAL transport
  state: true iff the active transport is non-loopback plain HTTP,
  independent of consent (`allow_insecure_http` is the persisted
  permission; a remote-HTTP profile can only be active WITH it, so
  `insecure` can never launder reality — remote HTTP in use with
  `insecure:false` is unreachable).
- `username` — non-secret (the settings form prefills it; status
  surfaces should display only `host`); `has_secret` — stored
  credential exists (bool, never the value).
- `status` — `connecting` | `connected` | `unreachable` |
  `auth_required` | `auth_failed` | `banned` | `tls_untrusted` |
  `tls_hostname` | `secrets_unavailable` | `insecure_http` |
  `invalid_configuration` | `backend_error`; `detail` — fixed short
  string, never reflects request payloads.
- `epoch` — backend epoch (monotonic per daemon process; resets on
  daemon restart).

### connection.test (one-shot probe; NO state change, NO torrent mutations)

```json
{"type":"connection.test","id":11,"url":"https://qbittorrent.home.arpa",
 "username":"clement","password":"…","tls_mode":"system"}
```
Key rules (schema-level): `url` (required, ≤ 2048 bytes, any scheme
shape — semantic validation is the handler's, answering
`invalid_configuration`); `username` (optional, ≤ 64 runes);
`password` (optional, 1–256 bytes) **xor** `use_stored_password`
(boolean, optional — both present is `invalid_message`); `tls_mode`
(required, `system` | `pin`; the file-only `ca` mode is not settable
over IPC); `pin` (optional, exactly 64 lowercase hex, semantically
required with `tls_mode:"pin"`); `allow_insecure_http` (optional
boolean, absent = false). With neither `password` nor
`use_stored_password` the probe is anonymous (no login; the
localhost-bypass path).

Response ok (exact key set; version fields iff ok):
```json
{"type":"connection.test","protocol":1,"id":11,"result":"ok","status":"connected",
 "host":"qbittorrent.home.arpa","transport":"https",
 "app_version":"v5.2.3","webapi_version":"2.15.1"}
```
Response failed:
```json
{"type":"connection.test","protocol":1,"id":11,"result":"failed","status":"tls_untrusted",
 "host":"qbittorrent.home.arpa","transport":"https","offered_fingerprint":"<64 hex>","detail":""}
```
`offered_fingerprint` is present iff a TLS certificate was presented
and rejected — it is the SHA-256 of the offered leaf certificate and
enables the explicit trust/pin flow (the certificate bytes themselves
never cross IPC). The test performs at most one login and two version
probes with a discarded cookie jar; a failed test never alters the
active backend, the stored profile or the stored secret.

### connection.configure (activate a profile — switches the epoch)

```json
{"type":"connection.configure","id":12,"url":"https://qbittorrent.home.arpa",
 "username":"clement","secret_action":"replace","password":"…","tls_mode":"pin","pin":"<64 hex>"}
```
Same key rules as `connection.test`, plus required `secret_action`:
`keep` (stored secret unchanged), `replace` (requires `password`) or
`delete` (stored secret removed) — an empty or abandoned form can
never erase or expose a secret. Activation order: validate URL/policy →
secret op via the provider → atomic `connection.json` write → backend
epoch switch (refused with `mutations_pending` while any mutation is
in flight) → subscribers receive the old torrents as removals, then a
fresh full sync of the new backend.

Accepted:
```json
{"type":"connection.configured","protocol":1,"id":12,"epoch":2,
 "mode":"remote","host":"qbittorrent.home.arpa","transport":"https"}
```
Rejected (connection stays open; no payload echoed):
```json
{"type":"connection.rejected","protocol":1,"id":12,"code":"insecure_http"}
```
| Code | Condition |
|---|---|
| invalid_url | URL/pin/tls-mode combination failed semantic validation |
| insecure_http | non-loopback HTTP without the explicit `allow_insecure_http` acknowledgement |
| pin_unknown | pin fingerprint has no captured certificate (re-test first) |
| secrets_unavailable | secret provider failed (missing/locked) on replace/delete |
| mutations_pending | a mutation is in flight; retry after it settles |
| storage_error | the atomic config write failed |

Contract fixtures: `contracts/ipc/v1/connection-status.txt`,
`connection-test.txt`, `connection-configure.txt`.

Client rules: same lockstep discipline (one request in flight,
id-matched); after a `connection.configured` frame, state clients
should expect the next snapshot to be a full rebuild (the daemon
publishes removals + fresh full sync) and MUST drop any cached torrent
rows on `torrent.snapshot.begin` as usual. The password field must be
cleared from QML immediately after the frame is written.

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
| unsupported_message | After hello, a valid message type other than health/system.status/dashboard.status/torrent.subscribe/torrent.pause/torrent.resume/torrent.add/torrent.remove/connection.status/connection.test/connection.configure, including another hello; also a second torrent.subscribe on an already-subscribed connection (error only, connection stays open) |

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
produced from cached state, so they arrive promptly — the exceptions
are the v1.2 mutation stage-1 response (one bounded ≤ 5 s backend
submission inline), the v1.4 `connection.test` response (bounded
≤ 8 s one-shot probe) and `connection.configure` (bounded secret op +
atomic write; the backend switch itself is asynchronous) — all under
the lockstep discipline. The degraded `qbittorrent:"unavailable"`
shape is the answer whenever the
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
  `mutation.accepted` as "submitted", never as success; handle BOTH
  legal frame orders (a `mutation.result` push may precede its
  `mutation.accepted`) by correlating terminal pushes with mutation ids
  and buffering unknown ids in a bounded early-result map; clear all
  pending/early state on disconnect and re-derive from the fresh
  snapshot; never auto-retry `torrent.remove` (fresh user confirmation
  required); render `timeout` results as ambiguous, not failed; do not
  depend on receiving result pushes — resolve stale pendings with a
  same-ref watchdog query (recorded outcome, no execution; never a
  fresh ref).
- Never send qBittorrent data, credentials, or derived secrets; this
  protocol carries none.

## Verification

See docs/DEVELOPMENT.md for executable checks. Contract tests use real Unix
sockets, including exact/oversize frames, UTF-8/duplicate-key rejection,
permissions, collision/replaced-path protection, client bounds/timeouts,
shutdown and reconnect, and the `system.status` ok/unavailable paths.
Example frames live in contracts/ipc/v1/ and are consumed by tests so
examples cannot silently drift.
