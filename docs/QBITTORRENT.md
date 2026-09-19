# OmaTorrent — qBittorrent Capability Matrix

Status: VERIFIED (Phase 0, 2026-09-18) — every entry carries its source and
version range. Classifications: CONFIRMED (verified live against the
installed backend), VERSION DEPENDENT (wiki-documented, minimum version
above the 2.0 baseline), UNCERTAIN (works locally but not documented in the
wiki — treat as untrusted for compatibility claims), NOT NEEDED BEFORE 1.0.

## Anchor facts (verified 2026-09-18, live probes)

- FACT: Workstation backend is qbittorrent-nox 5.2.3 (Arch package
  qbittorrent-nox 5.2.3-3), WebUI bound to 127.0.0.1:8080 with
  `WebUI\LocalHostAuth=false` (localhost auth bypass).
  SOURCE: `pacman -Q qbittorrent-nox`, `qbittorrent-nox --version`,
  `ss -tlnp`, qBittorrent.conf (non-secret keys only).
- FACT: `GET /api/v2/app/version` → `v5.2.3`;
  `GET /api/v2/app/webapiVersion` → `2.15.1`.
  SOURCE: live curl probes 2026-09-18.
- FACT: With localhost auth bypass, v2 endpoints answer 200 without a SID
  cookie. Login is still the general-case auth path (see adapter rules).
- FACT: `sync/maindata?rid=0` returns `full_update`, `rid`, `server_state`,
  `tags`, `torrents`, `trackers`; a follow-up `rid=1` request returns only
  deltas (observed: empty torrent delta after no changes).
  IMPLICATION: incremental state sync is real and is the 0.2 mechanism;
  Phase 0 uses a single rid=0 snapshot only.
- FACT: `server_state` includes `free_space_on_disk`, `connection_status`,
  `dl_info_speed`, `up_info_speed`, `use_alt_speed_limits`, rate limits.
  SOURCE: live probe key dump.
- FACT: HTTP 403 on login = IP banned for too many failed attempts; login
  requires Referer/Origin matching the Host header.
  SOURCE: official wiki authentication section.
- FACT: 3 real torrents present during probes; `torrents/info` field set
  includes `state`, `dlspeed`, `upspeed`, `progress`, `eta`, `category`,
  `tags`, `ratio`, sizes, limits.
  SOURCE: live probe (keys only recorded; names/content not stored).

## Capability matrix

Baseline: WebAPI 2.0 unless noted. "Live" = verified on 2.15.1 (2026-09-18).

| Capability | Endpoint(s) | Min WebAPI | Class | Phase | Source |
|---|---|---|---|---|---|
| Authentication | `POST auth/login` | 2.0 | CONFIRMED (general path; live dev uses localhost bypass) | 0 | wiki + live 200-no-SID |
| Auth ban distinction | login 403 | 2.0 | CONFIRMED (documented) | 0 | wiki |
| App version probe | `GET app/version` | 2.0 | CONFIRMED (live) | 0 | live |
| WebAPI version probe | `GET app/webapiVersion` | 2.0 | CONFIRMED (live) | 0 | live |
| Build info | `GET app/buildInfo` | 2.3.0 | VERSION DEPENDENT (live OK) | later | wiki + live |
| Incremental state sync | `GET sync/maindata?rid=N` | 2.0 (free_space 2.1.1) | CONFIRMED (live, rid delta observed) | 0.2 | wiki + live |
| Transfer info | `GET transfer/info` | 2.0 | CONFIRMED (live) | 0 | live |
| Speed limits (read) | `GET transfer/{down,up}loadLimit` | 2.0 | CONFIRMED (live) | later | live |
| Speed limits (set) | `POST transfer/set{Down,Up}loadLimit` | 2.0 | VERSION DEPENDENT (documented, not exercised) | later | wiki |
| Torrent list | `GET torrents/info` | 2.0 (`hashes` 2.0.1) | CONFIRMED (live) | 0 | live |
| Torrent count | `GET torrents/count` | undocumented | UNCERTAIN (live OK; not wiki-documented) | 0.2 | live |
| Add magnet / torrent | `POST torrents/add` | 2.0 (`tags` 2.6.2; structured JSON response ≥ qbt 5.2.0) | CONFIRMED (live, disposable magnet; see mutation section) | 0.3 | wiki + live |
| Stop / start (pause / resume) | `POST torrents/{stop,start}` (`{pause,resume}` on WebAPI < 2.11.0) | 2.11.0 | CONFIRMED (live, disposable torrent; legacy aliases REMOVED on 5.x — live 404) | 0.3 | wiki + live + source |
| Delete | `POST torrents/delete` | 2.0 | CONFIRMED (live, disposable torrent; destructive-design rules apply) | 0.3 | wiki + live |
| Files list | `GET torrents/files` | 2.0 (`indexes` 2.8.2) | VERSION DEPENDENT (not exercised) | 0.3+ | wiki |
| File priorities | `POST torrents/filePrio` | 2.0 (multi 2.2.0) | VERSION DEPENDENT (not exercised) | 0.3+ | wiki |
| Categories (read) | `GET torrents/categories` | 2.1.1 | CONFIRMED (live, empty set) | 0.2 | wiki + live |
| Category create/assign | `createCategory`, `setCategory` | 2.0 (`savePath` 2.1.0) | VERSION DEPENDENT | 0.3+ | wiki |
| Tags (read) | `GET torrents/tags` | 2.3.0 | CONFIRMED (live, 1 tag) | 0.2 | wiki + live |
| Tags (mutate) | `addTags`/`removeTags` | 2.3.0 | VERSION DEPENDENT | 0.3+ | wiki |
| Tags (setTags) | `POST torrents/setTags` | undocumented | UNCERTAIN (not exercised) | 0.3+ | absence in wiki |
| Trackers (read) | `GET torrents/trackers` | 2.0 (fields 2.2.0) | VERSION DEPENDENT (not exercised live) | 0.2 | wiki |
| Trackers (mutate) | `addTrackers`/`editTracker`/`removeTrackers` | 2.0 / 2.2.0 / 2.2.0 | VERSION DEPENDENT | 0.3+ | wiki |
| Queue ordering | `torrents/{in,de}creasePrio`, `{top,bottom}Prio` | 2.0 | VERSION DEPENDENT (queueing optional pref) | 0.3+ | wiki |
| Ratio/seeding limits | `setShareLimits` | 2.0.1 | VERSION DEPENDENT | 0.3+ | wiki |
| Free space | `server_state.free_space_on_disk` | 2.1.1 | CONFIRMED (live key present; value observed ~1.7 TB and served on the wire 0.4) — semantics: free space on the disk of the DEFAULT save path; other save paths are NOT reflected | 0.4 | wiki + live |
| Preferences (read/set) | `app/preferences`, `app/setPreferences` | 2.0 (fields vary) | CONFIRMED (live read, 223 keys) | 0.4+ | wiki + live |
| Rename file/folder | `renameFile`/`renameFolder` | 2.4.0 / 2.8.0 | NOT NEEDED BEFORE 1.0 | — | wiki |
| Peer details | `sync/torrentPeers` | 2.0 | NOT NEEDED BEFORE 1.0 | — | wiki |

Mutation endpoints were exercised against the live workstation backend on
2026-09-19 ONLY through a disposable research torrent (random-infohash
magnet that can never resolve metadata or create files) and no-op probes
on hashes that do not exist; the user's real torrents were verified
untouched before and after (torrent count 3 → 3). See the mutation
section below.

## Mutation endpoints — confirmed behavior (Phase 0.3 research, 2026-09-19)

Live-verified against qbittorrent-nox 5.2.3 / WebAPI 2.15.1, cross-checked
with the official wiki (4.1-era and 5.0-era pages) and the qBittorrent
source tags release-5.0.0/5.1.0/5.2.0/5.2.3.

### Stop / start (the modern pause / resume)

- FACT: `POST /api/v2/torrents/stop` and `POST /api/v2/torrents/start`
  take a form/query field `hashes` — multiple hashes separated by `|`, or
  the keyword `all`. OmaTorrent always sends EXACTLY ONE hash and never
  `all` (blast-radius rule).
  SOURCE: wiki (both page eras); live form-encoded probes.
- FACT: both return **HTTP 200 with an empty body in all scenarios** —
  including unknown hashes (silent no-op, live-verified) and
  already-stopped/already-running torrents (idempotent no-op,
  live-verified twice each).
  IMPLICATION: the HTTP response carries NO per-torrent information;
  the ONLY truthful confirmation of a stop/start is the observed state
  in sync/maindata (observed `stoppedDL` < 1 s after stop, `metaDL`
  after start, live).
- FACT: `torrents/pause` and `torrents/resume` are **REMOVED in
  qBittorrent 5.x** — live `404` with body `Endpoint does not exist` on
  5.2.3; source-verified absent from the API controller in
  release-5.0.0/5.1.0/5.2.3 (only `startAction`/`stopAction` are
  registered).
  SOURCE: live probe + qBittorrent source `src/webui/api/torrentscontroller.h`.
- FACT: `stop`/`start` were introduced with the 5.0 rename
  (WebAPI 2.11.0; torrent states `stopped*` replaced `paused*`, filter
  `stopped` replaced `paused`).
  SOURCE: qBittorrent 5.0 news, qbittorrent-api library docs.
  IMPLICATION (version handling): the adapter uses `stop`/`start` when
  the probed WebAPI version is ≥ 2.11.0 and falls back to `pause`/`resume`
  on older 4.x backends; the fallback path is fixture-tested only (this
  workstation cannot exercise it live).
- FACT: stopping a `metaDL` torrent works (observed `metaDL` →
  `stoppedDL`); `stoppedDL`/`stoppedUP` normalize to the daemon's
  `paused`.

### Add magnet

- FACT: `POST /api/v2/torrents/add` with a form field `urls` (magnet URI)
  succeeds with **200 and a structured JSON body** on qBittorrent ≥ 5.2.0:
  `{"added_torrent_ids":["<hash>"],"failure_count":0,"pending_count":0,"success_count":1}`.
  SOURCE: live probe ×3; introduced in release-5.2.0
  (`src/webui/api/torrentscontroller.cpp`).
  IMPLICATION: on modern backends acceptance is strong evidence — the
  infohash is echoed back. The daemon still parses the magnet's `xt`
  itself and cross-checks (never trusts the echo for identity).
- FACT: the torrent appears in `torrents/info` **immediately** after the
  200 (observed `queuedDL` on the first post-add poll, `metaDL` ~2 s
  later). Appearance in sync/maindata is likewise prompt.
  IMPLICATION: "HTTP success ⇒ torrent exists" held live, but the daemon
  still confirms via the sync path — async acceptance is the documented
  general case (metadata retrieval continues in the background; the
  torrent object exists first).
- FACT: adding a torrent that is **already present returns 409 Conflict**
  (body `Conflict`), NOT a silent success — live-verified.
- FACT: a malformed magnet (non-URL string, or `magnet:?xt=urn:btih:NOTHEX`)
  also returns **409 Conflict** — live-verified.
  IMPLICATION: the daemon validates magnet structure itself before
  submitting (deterministic local rejection) and treats a 409 on an
  otherwise-valid magnet as duplicate/rejected by backend.
- FACT: on pre-5.2.0 backends the documented response is 200 with plain
  body `Ok.` (and `415` for an invalid torrent FILE upload); duplicate
  adds are NOT distinguishable from the response there.
  SOURCE: 4.1-era wiki.
  IMPLICATION: on legacy backends duplicate detection is only possible
  via state reconciliation (hash already in sync state before submit).
  Class: VERSION DEPENDENT.
- NOT NEEDED BEFORE 1.0 (documented, deliberately unused by 0.3):
  `paused` (add stopped), `savepath`, `category`, `tags`, `skip_checking`,
  `root_folder`, `rename`, `upLimit`/`dlLimit`, `sequentialDownload`,
  `firstLastPiecePrio`, `autoTMM`, `contentLayout`, `stopCondition`,
  `downloadPath`; `.torrent` file upload via the `torrents` multipart
  field (non-goal for 0.3); the `cookie` field was REMOVED in WebAPI
  2.11.3.

### Delete

- FACT: `POST /api/v2/torrents/delete` takes `hashes` (same format as
  stop/start) and **`deleteFiles` (bool)** — "If set to true, the
  downloaded data will also be deleted, otherwise has no effect."
  SOURCE: wiki parameter table (both eras, verbatim).
- FACT: returns **200 with an empty body in all scenarios** — including
  unknown hashes (silent no-op with `deleteFiles=false` AND
  `deleteFiles=true`, both live-verified).
  IMPLICATION: delete never reports per-hash failure; disappearance is
  confirmed ONLY via state (observed: `torrents/info?hashes=` → `[]`
  immediately; sync/maindata delta carried `torrents_removed:[hash]`).
- FACT: `deleteFiles=false` removes only the torrent from the session
  (data kept); `deleteFiles=true` also removes downloaded data
  (live-exercised only on the file-less disposable research torrent).
- FACT: a removed hash can be re-added afterwards (observed accepted).
- DESIGN RULES (binding for OmaTorrent): the adapter ALWAYS sends
  `deleteFiles` explicitly (`true`/`false`, never omitted — no reliance
  on any backend default); OmaTorrent never sends multiple hashes or the
  `all` keyword to a destructive endpoint; the IPC layer requires an
  explicit boolean and rejects ambiguous/missing intent.

### Cross-cutting facts

- FACT: `GET torrents/info?hashes=<h>` returns `[]` for unknown hashes —
  the direct existence probe (live). The daemon's committed sync state
  serves the same check without extra I/O.
- UNCERTAIN: the exact WebAPI minor version that introduced the
  structured `add` response (release-5.2.0 is source-confirmed; its
  API-version constant was not extractable from the tags) — recorded as
  "qBittorrent ≥ 5.2.0". The 409 body text varying beyond `Conflict` is
  likewise unverified.
- Live-test disclosure (2026-09-19): disposable magnet
  `omatorrent-disposable-research` (random infohash
  `7a3f9c1d…`, unresolvable — no peers, no metadata, no files); the
  user's real torrent count was 3 before and after; the disposable was
  removed with both `deleteFiles=false` and `deleteFiles=true` (no files
  existed).

## sync/maindata — confirmed behavior (Phase 0.2 research, 2026-09-18)

Live-verified against qbittorrent-nox 5.2.3 / WebAPI 2.15.1 (read-only
probes, cookie-jar session) and cross-checked with the official wiki.

- FACT: rid semantics — rid=0 (or omitted) yields `full_update:true` with
  a complete `torrents` map and full `server_state`; the response carries
  the NEXT rid. Sending the last-received rid with the SAME session cookie
  yields a delta (`full_update` absent/false, `torrents` empty when
  nothing changed) and an incremented rid (observed 1 → 2).
  SOURCE: live probes with curl cookie jar; wiki: "If the given rid is
  different from the one of last server reply, full_update will be true".
- FACT: **rid tracking is session-scoped.** Without a session cookie every
  request is a fresh session: repeated same-rid requests each return
  `full_update:true` and the same rid (observed with bare curl under the
  localhost auth bypass). The bypass DOES set a `QBT_SID_<port>` cookie
  (HttpOnly, SameSite=Lax) on responses — persisting the cookie jar makes
  incremental sync work without credentials on this deployment.
  IMPLICATION: the adapter must retain cookies across requests (Go
  http.Client cookie jar); with credentials the SID from auth/login serves
  the same role.
- FACT: delta `torrents` entries are PARTIAL objects — only changed fields
  (wiki example: `{"state":"pausedUP"}`; live full update carries 68
  fields per torrent). Full-update entries are complete objects with the
  `torrents/info` field set.
  IMPLICATION: the daemon must field-merge deltas into its normalized
  model, never replace entries wholesale on delta responses.
- FACT: `torrents_removed` is an array of hashes removed since the last
  request with that session. Observed empty/absent when nothing changed.
- FACT: `server_state` (26 keys live: speeds, connection_status,
  free_space_on_disk, dht_nodes, rate limits, use_alt_speed_limits, …) is
  present on full updates and ABSENT on a no-change delta (observed
  `null`). Whether a state change delivers partial or full server_state
  is UNCERTAIN (cannot force a change read-only) — the adapter
  defensively merges present fields and keeps last-known-good.
- FACT: torrent `state` values on 5.2.3 use the `stalledUP`/`stalledDL`
  family (live: stalledUP, stalledUP, stalledDL); wiki enumerates:
  error, missingFiles, uploading, pausedUP, queuedUP, stalledUP,
  checkingUP, forcedUP, allocating, downloading, metaDL, pausedDL,
  queuedDL, stalledDL, checkingDL, forcedDL, checkingResumeData, moving,
  unknown. (qBittorrent 5.x additionally exposes stoppedUP/stoppedDL
  aliases in some responses — treat unknown values as `unknown`, never
  reject.)
- IMPLICATION (restart/resync): a qBittorrent restart forgets the
  session/rid → next request returns `full_update:true`. The daemon
  treats ANY `full_update:true` as a rebuild signal and never assumes
  its rid survived.
- Non-goal confirmed: `sync/torrentPeers` not needed for 0.2.

## Adapter rules derived from this matrix

1. Auth: `POST auth/login` (Referer/Origin matching Host), SID cookie reuse;
   treat 403-after-failures as BAN (distinct from bad credentials). Localhost
   deployments may omit credentials entirely (bypass) — the adapter must
   work in both modes without logging secrets.
2. Always probe `app/webapiVersion` at connect; compare against the
   capability matrix before using any endpoint above the 2.0 baseline.
3. State sync: rid-based `sync/maindata` deltas with a persistent cookie
   jar; the daemon rebuilds from `full_update:true` responses (rid=0,
   session loss, backend restart) instead of re-polling on its own.
4. Minimum supported WebAPI version: **PROPOSED 2.3.0** (highest minimum
   among endpoints OmaTorrent needs before 1.0: tags). The reference
   deployment is 2.15.1; a formal compat matrix is a later-phase task —
   do not claim broader support than tested.
5. Mutations (0.3): stop/start when WebAPI ≥ 2.11.0, pause/resume
   fallback below it; an EMPTY (unprobed) version defaults to the modern
   endpoints, a present-but-unparseable version falls back to the legacy
   ones — either wrong guess fails visibly (404 → `backend_rejected`),
   never silently; exactly one hash per mutation request, never
   `all`; `deleteFiles` always explicit; mutation success is confirmed
   ONLY through the sync state (HTTP 200 carries no per-torrent truth);
   the daemon pre-validates magnets (scheme, `xt` urn, 40/64-hex btih)
   so local rejections are deterministic before any backend call.

## Sources

- Official WebUI API documentation (qBittorrent wiki, "WebUI API
  (qBittorrent 4.1)", incl. API changelog):
  https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-(qBittorrent-4.1)
- Live probes against installed qbittorrent-nox 5.2.3 / WebAPI 2.15.1 on
  this workstation, 2026-09-18 (read-only endpoints only).
- Live mutation probes (disposable random-infohash magnet + no-op
  unknown-hash calls only), 2026-09-19 — see the mutation section.
- qBittorrent source tags release-5.0.0 / 5.1.0 / 5.2.0 / 5.2.3
  (`src/webui/api/torrentscontroller.{h,cpp}`, `webapplication.cpp`)
  for endpoint registration and the structured add response.
- qbittorrent-api Python library docs (WebAPI 2.11.0 stop/start and
  state-rename attribution):
  https://qbittorrent-api.readthedocs.io/en/latest/apidoc/torrents.html
