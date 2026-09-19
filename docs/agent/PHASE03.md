# Phase 0.3 — Essential torrent actions (record)

Tracking: GitHub issue #5; branch `feat/phase03-essential-actions`.
Status: IMPLEMENTED, PR open for external review (NOT merged). Date: 2026-09-19.
Base: main @ 7b2a914 (Phase 0.2 merged at 3e090d5).

## Delivered

```
Panel (QML, presentation only)
  intents: torrent.pause / resume / add / remove(+delete_files)
  confirmations (remove / remove+delete files distinct, urgent) + results
    ↓ IPC v1.2 (ADR-0006: staged mutations, strict schemas, ref replay)
internal/mutate (validation vs committed state, bounded submission,
  version-gated endpoint selection, ref ring, state-derived results)
    ↓
internal/qbittorrent (stop/start [pause/resume legacy], torrents/add,
  torrents/delete with EXPLICIT deleteFiles; one hash, never `all`)
    ↓
qBittorrent WebAPI 2.15.1 (live-verified semantics, docs/QBITTORRENT.md)
```

- Request accepted ≠ mutation confirmed: acceptance and confirmation are
  separate stages; confirmation derives exclusively from committed sync
  state (qBittorrent mutation endpoints return 200-empty in all
  scenarios — the HTTP response is never treated as per-torrent truth).
- `delete_files` is a required boolean at every layer; ambiguity is a
  protocol violation (connection closes); the adapter never relies on a
  backend default and never infers intent.
- Replay safety: completed refs return their recorded terminal outcome —
  a retried remove can never execute twice. Per-process limit documented
  (not durable across daemon restarts).
- Live mutation validation used ONLY a disposable random-infohash magnet
  (never resolvable — no metadata, no files) removed with BOTH
  deleteFiles variants; user's real torrent count verified 3 → 3 around
  every live run.

## Research highlights (docs/QBITTORRENT.md, mutation section)

- stop/start required on qBittorrent 5.x — pause/resume endpoints were
  REMOVED in 5.0 (live 404 + source-verified across release-5.0.0…5.2.3);
  adapter falls back to pause/resume below WebAPI 2.11.0 (fixture-tested
  legacy path).
- torrents/add on ≥ 5.2.0 returns structured JSON with
  added_torrent_ids; the torrent object exists immediately; duplicates
  and malformed magnets are refused with 409 (live-verified).
- torrents/delete is 200-empty always; removal reconciles through
  sync/maindata torrents_removed (live-verified).

## Evidence matrix (executed 2026-09-19)

| Check | Result |
|---|---|
| `go build ./...` / `go vet ./...` / `gofmt -l .` | PASS |
| `go test -race ./...` (config/ipc/mutate/qbittorrent/state) | PASS |
| Harness validation (82/82) + guard hook (39/39) | PASS |
| `omarchy plugin validate plugins/local.omatorrent` | PASS |
| `tools/test_quickshell.sh` incl. v1.2 lifecycle (10 stages, disposable torrent) | PASS |
| Live mutation research probes (disposable magnet + no-op unknown hashes) | PASS (user torrents untouched, 3 → 3) |
| Daemon under systemd: 0.3.0-phase03 start, sync, probe | PASS |
| Shell restart → new panel loads, zero QML journal errors | PASS |
| Panel live: rows, hover actions, +, header | PASS (screenshot) |
| Panel degraded: daemon stopped → truthful banner, rows retained, actions hidden | PASS (screenshot) |
| Panel recovery: daemon restarted → resubscribe + full snapshot | PASS (screenshot) |
| Journal audit across open/degraded/recovery cycle | PASS (zero OmaTorrent errors) |
| Secret scan | PASS |
| Confirm-strip / add-input visual appearance | NOT OBSERVABLE (no input-synthesis tool on this host; logic covered by smoke + schema tests; layouts reuse validated components) |
| Destructive remove-with-files on a torrent WITH real files | NOT RUN (no safe disposable target with files exists; fixture + no-file disposable + contract enforcement cover the path) |
| Legacy (pre-2.11.0) backend paths live | NOT RUN (single 5.2.3 backend; fixture-tested) |

## Bugs caught by validation

- Live smoke stage 3 exposed: add-replay matched on hash (absent on add
  requests) instead of the magnet URL → spurious ref_conflict. Fixed in
  internal/mutate (matches() now compares URL for add), regression test
  added (TestReplayAddMatchesOnURL).

## Review verdicts

(pending — to be recorded after architecture/security/QA reviews)

## Known limitations

- Ref deduplication is per-daemon-process; a daemon restart forgets
  completed refs (client obligations forbid auto-replay of destructive
  refs across restarts).
- Base32 btih magnets are rejected (invalid_url) — hex-only in 0.3.
- mutation.result timeout is ambiguous by design; the panel surfaces it
  as "result unknown" rather than failure.
- Panel pending overlay is per-hash; a second mutation on the same hash
  before the first result replaces the overlay (cosmetic; results still
  reconcile correctly).
- Legacy pause/resume path is fixture-tested only (no 4.x backend).
