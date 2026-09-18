---
name: omatorrent-qbittorrent-api
description: Use whenever OmaTorrent work interacts with qBittorrent — calling or modeling the WebUI API, designing adapter methods, writing fixtures or contract tests, or debugging backend behavior. Requires verifying the API/version contract (WebAPI version vs application version) from current official sources before relying on any endpoint, and prefers sync/maindata incremental synchronization where appropriate.
---

# OmaTorrent qBittorrent API Rules

qBittorrent is OmaTorrent's only engine (before 1.0). Its WebUI API is
version-sensitive: the **WebAPI version** and the **qBittorrent application
version** are different numbers and must both be recorded for every fact.

## Before relying on any endpoint or field

1. Verify it in the current official qBittorrent WebUI API documentation and
   its changelog (community posts are not a contract).
2. Record in docs/QBITTORRENT.md: endpoint, minimum WebAPI version, qBittorrent
   version range, and observed behavior. If unverified: mark UNKNOWN, do not
   guess.
3. Delegate deep research to the omatorrent-qbt-researcher agent; it answers
   as FACT / SOURCE / VERSION RANGE / IMPLICATION / RECOMMENDATION.

## Adapter rules

- All qBittorrent access lives in omatorrent-service's adapter — never QML.
- Login flow: `POST /api/v2/auth/login`; keep the session cookie; treat
  `403` as banned (too many failed attempts) distinctly from bad credentials.
- Prefer **incremental state synchronization** via `GET /api/v2/sync/maindata`
  with the `rid` response-id: first call (rid=0 or absent) returns the full
  snapshot; later calls return only changes (`full_update` semantics verified
   per docs). Do not re-poll complete torrent lists when maindata suffices.
- Capability probes come first at connect: `app/version`,
  `app/webapiVersion`; gate features on the WebAPI version matrix.
- Distinguish error classes: network failure vs auth failure vs API-version
  mismatch vs malformed response — each has a different recovery path.
- Fixtures for contract tests carry the exact qBittorrent/WebAPI version they
  were captured from.
