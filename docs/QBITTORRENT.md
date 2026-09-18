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
| Add magnet / torrent | `POST torrents/add` | 2.0 (`tags` 2.6.2) | VERSION DEPENDENT (documented, not exercised) | 0.3 | wiki |
| Pause / resume | `POST torrents/{pause,resume}` | 2.0 | VERSION DEPENDENT (documented, not exercised) | 0.3 | wiki |
| Delete | `POST torrents/delete` | 2.0 | VERSION DEPENDENT (documented, not exercised; destructive-design rules apply) | 0.3 | wiki |
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
| Free space | `server_state.free_space_on_disk` | 2.1.1 | CONFIRMED (live key present) | 0.4+ | wiki + live |
| Preferences (read/set) | `app/preferences`, `app/setPreferences` | 2.0 (fields vary) | CONFIRMED (live read, 223 keys) | 0.4+ | wiki + live |
| Rename file/folder | `renameFile`/`renameFolder` | 2.4.0 / 2.8.0 | NOT NEEDED BEFORE 1.0 | — | wiki |
| Peer details | `sync/torrentPeers` | 2.0 | NOT NEEDED BEFORE 1.0 | — | wiki |

Mutation endpoints were deliberately NOT exercised against the live
workstation backend (user's real torrents); their Phase 0 status is
documented-only. Adapter behavior for them is fixture-tested when their
phase arrives.

## Adapter rules derived from this matrix

1. Auth: `POST auth/login` (Referer/Origin matching Host), SID cookie reuse;
   treat 403-after-failures as BAN (distinct from bad credentials). Localhost
   deployments may omit credentials entirely (bypass) — the adapter must
   work in both modes without logging secrets.
2. Always probe `app/webapiVersion` at connect; compare against the
   capability matrix before using any endpoint above the 2.0 baseline.
3. State sync: rid-based `sync/maindata` deltas; never re-poll full data.
4. Minimum supported WebAPI version: **PROPOSED 2.3.0** (highest minimum
   among endpoints OmaTorrent needs before 1.0: tags). The reference
   deployment is 2.15.1; a formal compat matrix is a later-phase task —
   do not claim broader support than tested.

## Sources

- Official WebUI API documentation (qBittorrent wiki, "WebUI API
  (qBittorrent 4.1)", incl. API changelog):
  https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-(qBittorrent-4.1)
- Live probes against installed qbittorrent-nox 5.2.3 / WebAPI 2.15.1 on
  this workstation, 2026-09-18 (read-only endpoints only).
