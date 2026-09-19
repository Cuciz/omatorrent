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
# Exit 0 = >=2 id-matched status exchanges + verified subscription + full
# snapshot + rename model OK. Requires: omatorrent-service running
# (default socket), quickshell (qs).
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
        maybeDone()
        break
      case "torrent.delta":
      case "error":
        break
    }
  }

  function maybeDone() {
    if (matched >= 2 && snapshotBegin === 1 && snapshotEnd === 1 && renameTestDone) {
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
      root.maybeDone()
      if (!root.helloSent && sock.connected) {
        root.helloSent = true
        root.send({ type: "hello", protocol: 1 })
        print("OTQS-SENT hello")
      } else if (root.helloSent && root.pendingKind === "") {
        root.requestStatus()
      } else if (root.helloSent && root.pendingKind !== "" && Date.now() - root.pendingSince > 6000) {
        print("OTQS-FAIL stuck pending " + root.pendingKind)
        Qt.quit()
      }
    }
  }
  Timer { interval: 20000; running: true; onTriggered: Qt.quit() } // hard timeout
}
QMLEOF
sed -i "s|SOCKPATH|${SOCK}|g" "$DIR/shell.qml"

echo "== isolated quickshell IPC smoke against $SOCK =="
timeout 25 qs -p "$DIR" 2>&1 | grep -E "OTQS-" | tee "$DIR/out.log" >&2

status=$(grep -c 'OTQS-READ: {"type":"system.status"' "$DIR/out.log" || true)
subd=$(grep -c 'OTQS-SUBSCRIBED' "$DIR/out.log" || true)
snap=$(grep -c 'OTQS-SNAPSHOT' "$DIR/out.log" || true)
rename=$(grep -c 'OTQS-RENAME-OK' "$DIR/out.log" || true)
fail=$(grep -c 'OTQS-FAIL' "$DIR/out.log" || true)
doneMarker=$(grep -c 'OTQS-DONE' "$DIR/out.log" || true)

echo "matched-status=$status subscribed=$subd snapshots=$snap rename-model=$rename failures=$fail"
if [ "$fail" -eq 0 ] && [ "$doneMarker" -ge 1 ] && [ "$subd" -ge 1 ] && [ "$rename" -ge 1 ]; then
  echo "PASS: one-in-flight discipline (status then subscribe) + full v1.1 snapshot + deterministic rename-order model"
  exit 0
fi
echo "FAIL: expected OTQS-DONE with subscribed + rename-model and no OTQS-FAIL"
exit 1
