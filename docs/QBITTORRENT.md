# OmaTorrent — qBittorrent Capability Matrix

Status: STARTED — maintained by the omatorrent-qbt-researcher agent. Every
entry must carry its source and version range; UNKNOWN means not yet
verified, never "probably works".

## Anchor facts (verified 2026-09-18)

- FACT: Official WebUI API documentation (qBittorrent wiki, "WebUI API
  (qBittorrent 4.1)") documents the `/api/v2/...` methods and the API
  changelog up to WebAPI 2.8.3, covering qBittorrent 4.1–4.6.x.
  SOURCE: https://github.com/qbittorrent/qBittorrent/wiki/WebUI-API-(qBittorrent-4.1)
  IMPLICATION: the wiki page is the contract baseline; newer application
  versions (see below) must be cross-checked against the changelog/source.
- FACT: This workstation runs qbittorrent-nox 5.2.3 (local development
  backend). Its exact WebAPI version: UNKNOWN — probe with
  `/api/v2/app/webapiVersion` at Phase 0.
- FACT: `POST /api/v2/auth/login` (session cookie; requires Referer/Origin
  matching the Host), `GET /api/v2/app/version`,
  `GET /api/v2/app/webapiVersion`, `GET /api/v2/sync/maindata?rid=N`
  (incremental sync: first request returns full snapshot, subsequent rid
  values return changes) are documented in the official wiki.
  IMPLICATION: capability probe + incremental state sync are the intended
  0.1/0.2 mechanisms.
- FACT: HTTP 403 after repeated failed logins indicates a temporary ban
  (documented in the wiki's authentication section).
  IMPLICATION: adapter must classify auth-ban distinctly from bad
  credentials.

## Capability matrix (to be filled by the researcher)

| Endpoint | Purpose | Min WebAPI version | qBittorrent range | Verified | Source |
|---|---|---|---|---|---|
| auth/login | authenticate | 2.0 | wiki-documented | yes (wiki) | wiki |
| app/version | app version probe | 2.0? | UNKNOWN | no | — |
| app/webapiVersion | WebAPI version probe | 2.0? | UNKNOWN | no | — |
| sync/maindata | incremental state | UNKNOWN | UNKNOWN | no | — |
| torrents/* (mutations) | pause/resume/add/remove | UNKNOWN | UNKNOWN | no | — |

Cells marked UNKNOWN are research tasks, not assumptions. Minimum supported
qBittorrent version for 0.1: OPEN — decided with Phase 0 probe results.
