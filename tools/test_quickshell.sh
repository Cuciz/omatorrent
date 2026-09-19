#!/usr/bin/env bash
# Isolated Quickshell IPC smoke test: proves the Quickshell.Io Socket +
# SplitParser client path against the running omatorrent-service daemon,
# without touching the user's shell.
#
# Mirrors the production client discipline (docs/IPC.md): ONE request in
# flight at any time across ALL request types (pendingKind), cleared only
# by the matching id-verified response — status first, subscribe only
# after the status response, subscribed set only on torrent.subscribed.
# Also runs a deterministic rename-ordering model check mirroring the
# panel's source model (map + sorted order + rename repositioning).
#
# v1.2 mutation flow (Phase 0.3): exercises the full staged contract
# against a DISPOSABLE magnet (random infohash that can never resolve
# metadata or create files; removed with both delete_files variants at
# the end). The user's real torrents are never mutated.
#
# Exit 0 = >=2 id-matched status exchanges + verified subscription + full
# snapshot + rename model OK + all mutation stages OK. Requires:
# omatorrent-service running (default socket), quickshell (qs).
set -euo pipefail

SOCK="${OT_SOCKET:-${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/omatorrent/service.sock}"
DIR="$(mktemp -d /tmp/ot-qs-test.XXXXXX)"
trap 'rm -rf "$DIR"' EXIT

cat > "$DIR/shell.qml" <<'QMLEOF'
import QtQuick
import Quickshell
import Quickshell.Io

ShellRoot {
  id: root
  property bool helloSent: false
  property int pendingId: -1
  property string pendingKind: ""
  property real pendingSince: 0
  property int nextId: 1
  property bool subscribed: false
  property int matched: 0
  property int snapshotBegin: 0
  property int snapshotItems: 0
  property int snapshotEnd: 0
  property bool renameTestDone: false

  // ---- v1.2 mutation stages (all against a disposable torrent).
  property string ghostHash: ""   // random 40-hex: never resolvable, no data
  property int mutStage: 0        // 0=waiting for snapshot; advance via mutStep()
  property string mutWait: ""     // waiting for a pushed result action
  property int mutPass: 0
  property int mutTotal: 10

  function genHash() {
    var s = ""
    for (var i = 0; i < 40; i++) s += Math.floor(Math.random() * 16).toString(16)
    return s
  }

  function send(msg) { sock.write(JSON.stringify(msg) + "\n"); sock.flush() }

  function requestStatus() {
    if (pendingKind !== "") return
    pendingKind = "status"
    pendingId = nextId++
    pendingSince = Date.now()
    send({ type: "system.status", id: pendingId })
  }

  function requestSubscribe() {
    if (pendingKind !== "" || subscribed) return
    pendingKind = "subscribe"
    pendingId = nextId++
    pendingSince = Date.now()
    send({ type: "torrent.subscribe", id: pendingId })
  }

  function requestMutation(action, extra, refOverride) {
    if (pendingKind !== "" || !helloSent) return false
    pendingKind = "mutation"
    pendingId = nextId++
    pendingSince = Date.now()
    const ref = refOverride !== undefined ? refOverride : "smoke-" + mutStage
    send(Object.assign({ type: action, id: pendingId, ref: ref }, extra))
    return true
  }

  function mutStageDone(ok, label) {
    if (!ok) {
      print("OTQS-FAIL mutation stage " + mutStage + " (" + label + ")")
      Qt.quit()
      return
    }
    print("OTQS-MUT stage " + mutStage + " ok (" + label + ")")
    mutPass++
    mutStage++
    mutStep()
  }

  // One stage at a time; stages expecting a pushed result set mutWait
  // and advance when the result frame arrives.
  function mutStep() {
    if (pendingKind !== "" || mutWait !== "") return
    const magnet = "magnet:?xt=urn:btih:" + ghostHash + "&dn=otqs-disposable-smoke"
    switch (mutStage) {
      case 0: // wait for snapshot (handled in snapshot.end)
        return
      case 1: // add disposable magnet -> accepted + confirmed result
        if (requestMutation("torrent.add", { url: magnet })) mutWait = "torrent.add"
        break
      case 2: // duplicate add -> rejected duplicate
        if (requestMutation("torrent.add", { url: magnet })) mutWait = "!duplicate"
        break
      case 3: // replay the COMPLETED stage-1 ref -> result-with-id confirmed
        if (requestMutation("torrent.add", { url: magnet }, "smoke-1")) mutWait = "!replay"
        break
      case 4: // structurally invalid magnet -> rejected invalid_url (no close)
        if (requestMutation("torrent.add", { url: "magnet:?xt=urn:btih:NOTHEX&dn=x" })) mutWait = "!invalid_url"
        break
      case 5: // pause -> accepted + confirmed
        if (requestMutation("torrent.pause", { hash: ghostHash })) mutWait = "torrent.pause"
        break
      case 6: // resume -> accepted + confirmed
        if (requestMutation("torrent.resume", { hash: ghostHash })) mutWait = "torrent.resume"
        break
      case 7: // remove WITHOUT files -> confirmed absent
        if (requestMutation("torrent.remove", { hash: ghostHash, delete_files: false })) mutWait = "torrent.remove"
        break
      case 8: // re-add -> confirmed present (fresh ref, new attempt)
        if (requestMutation("torrent.add", { url: magnet })) mutWait = "torrent.add"
        break
      case 9: // remove WITH files (torrent has none) -> confirmed absent
        if (requestMutation("torrent.remove", { hash: ghostHash, delete_files: true })) mutWait = "torrent.remove"
        break
      case 10: // unknown hash -> rejected stale_torrent
        if (requestMutation("torrent.pause", { hash: "ffffffffffffffffffffffffffffffffffffffff" })) mutWait = "!stale_torrent"
        break
      default:
        maybeDone()
    }
  }

  // Deterministic rename-ordering check mirroring the panel's source
  // model (map + sorted order + rename repositioning).
  function runRenameModelTest() {
    var torrents = ({})
    var order = []
    function insertSorted(hash, item) {
      var lo = 0, hi = order.length
      while (lo < hi) {
        var mid = (lo + hi) >> 1
        if (torrents[order[mid]].name < item.name) lo = mid + 1
        else hi = mid
      }
      order.splice(lo, 0, hash)
    }
    torrents["hA"] = { hash: "hA", name: "Alpha torrent" }
    torrents["hB"] = { hash: "hB", name: "Zeta torrent" }
    insertSorted("hA", torrents["hA"])
    insertSorted("hB", torrents["hB"])
    if (order[0] !== "hA" || order[1] !== "hB") {
      print("OTQS-FAIL rename-model initial order " + order)
      return
    }
    // Rename B so it sorts BEFORE A ("Aardvark" < "Alpha"); the model
    // must reposition it.
    torrents["hB"] = { hash: "hB", name: "Aardvark torrent" }
    var i = order.indexOf("hB")
    if (i >= 0) order.splice(i, 1)
    insertSorted("hB", torrents["hB"])
    if (order[0] !== "hB" || order[1] !== "hA") {
      print("OTQS-FAIL rename order after rename " + order)
      return
    }
    print("OTQS-RENAME-OK")
    renameTestDone = true
  }

  function handle(line) {
    let msg
    try { msg = JSON.parse(line) } catch (e) { return }
    if (!msg || typeof msg.type !== "string") return
    switch (msg.type) {
      case "hello":
        requestStatus() // subscribe only AFTER the status completes
        break
      case "system.status":
        if (pendingKind !== "status" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingKind = ""
        pendingId = -1
        matched++
        print("OTQS-READ: " + line)
        if (!subscribed) requestSubscribe()
        break
      case "torrent.subscribed":
        if (pendingKind !== "subscribe" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingKind = ""
        pendingId = -1
        subscribed = true
        print("OTQS-SUBSCRIBED")
        break
      case "torrent.snapshot.begin":
        snapshotBegin++
        break
      case "torrent.snapshot.item":
        if (msg.torrent && typeof msg.torrent.hash === "string") snapshotItems++
        break
      case "torrent.snapshot.end":
        snapshotEnd++
        print("OTQS-SNAPSHOT items=" + snapshotItems)
        ghostHash = genHash()
        mutStage = 1
        mutStep()
        maybeDone()
        break
      case "mutation.accepted":
        if (pendingKind !== "mutation" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingKind = ""
        pendingId = -1
        // Accepted = submitted, not success; stages that expect a code or
        // a replay resolve here, confirmed-result stages wait for the push.
        if (mutWait === "!replay") {
          mutWait = ""
          mutStageDone(false, "replay answered accepted instead of result")
        }
        break
      case "mutation.rejected":
        if (pendingKind !== "mutation" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingKind = ""
        pendingId = -1
        if (mutWait === "!" + msg.code) {
          const code = msg.code
          mutWait = ""
          mutStageDone(true, "rejected " + code)
        } else {
          print("OTQS-FAIL stage " + mutStage + " rejected " + msg.code + " wanted " + mutWait)
          Qt.quit()
        }
        break
      case "mutation.result":
        if (typeof msg.id === "number") {
          // Replay answer carries the request id.
          if (pendingKind !== "mutation" || msg.id !== pendingId) return
          pendingKind = ""
          pendingId = -1
          if (mutWait === "!replay") {
            const ok = msg.status === "confirmed"
            mutWait = ""
            mutStageDone(ok, "replay result " + msg.status)
          }
          break
        }
        // Pushed terminal result: must be confirmed for the waiting stage.
        if (mutWait === msg.action && msg.hash === ghostHash) {
          const st = msg.status
          mutWait = ""
          mutStageDone(st === "confirmed", msg.action + " " + st)
        }
        break
      case "torrent.delta":
      case "error":
        break
    }
  }

  function maybeDone() {
    if (matched >= 2 && snapshotBegin === 1 && snapshotEnd === 1 && renameTestDone && mutPass >= mutTotal) {
      print("OTQS-DONE")
      Qt.quit()
    }
  }

  Socket {
    id: sock
    path: "SOCKPATH"
    connected: true
    parser: SplitParser {
      splitMarker: "\n"
      onRead: function (d) { root.handle(d) }
    }
    onError: function (e) { print("OTQS-ERROR " + e) }
  }

  Timer {
    interval: 400
    running: true
    repeat: true
    onTriggered: {
      if (!root.renameTestDone) root.runRenameModelTest()
      root.mutStep()
      root.maybeDone()
      if (!root.helloSent && sock.connected) {
        root.helloSent = true
        root.send({ type: "hello", protocol: 1 })
        print("OTQS-SENT hello")
      } else if (root.helloSent && root.pendingKind === "") {
        root.requestStatus()
      } else if (root.helloSent && root.pendingKind !== "" && Date.now() - root.pendingSince > 8000) {
        print("OTQS-FAIL stuck pending " + root.pendingKind)
        Qt.quit()
      }
    }
  }
  Timer { interval: 90000; running: true; onTriggered: { print("OTQS-FAIL hard timeout (stage " + mutStage + " wait " + mutWait + ")"); Qt.quit() } } // hard timeout
}
QMLEOF
sed -i "s|SOCKPATH|${SOCK}|g" "$DIR/shell.qml"

echo "== isolated quickshell IPC smoke against $SOCK =="
timeout 100 qs -p "$DIR" 2>&1 | grep -E "OTQS-" | tee "$DIR/out.log" >&2

status=$(grep -c 'OTQS-READ: {"type":"system.status"' "$DIR/out.log" || true)
subd=$(grep -c 'OTQS-SUBSCRIBED' "$DIR/out.log" || true)
snap=$(grep -c 'OTQS-SNAPSHOT' "$DIR/out.log" || true)
rename=$(grep -c 'OTQS-RENAME-OK' "$DIR/out.log" || true)
mut=$(grep -c 'OTQS-MUT stage' "$DIR/out.log" || true)
fail=$(grep -c 'OTQS-FAIL' "$DIR/out.log" || true)
doneMarker=$(grep -c 'OTQS-DONE' "$DIR/out.log" || true)

echo "matched-status=$status subscribed=$subd snapshots=$snap rename-model=$rename mutation-stages=$mut/10 failures=$fail"
if [ "$fail" -eq 0 ] && [ "$doneMarker" -ge 1 ] && [ "$subd" -ge 1 ] && [ "$rename" -ge 1 ] && [ "$mut" -ge 10 ]; then
  echo "PASS: one-in-flight discipline + v1.1 snapshot + rename model + v1.2 mutation lifecycle (disposable torrent, both delete_files paths, duplicate/replay/invalid/stale rejections)"
  exit 0
fi
echo "FAIL: expected OTQS-DONE with subscribed + rename-model + 10 mutation stages and no OTQS-FAIL"
exit 1
