#!/usr/bin/env bash
# Isolated Quickshell IPC smoke test: proves the Quickshell.Io Socket +
# SplitParser client path against the running omatorrent-service daemon,
# without touching the user's shell.
#
# Exercises the client discipline from docs/IPC.md (one request in
# flight, id matching, mismatched ids ignored) and the v1.1 subscription
# flow (torrent.subscribe → subscribed + bounded snapshot frames +
# delta frames; ADR-0005).
#
# Exit 0 = handshake + >=2 id-matched status exchanges + >=1 ignored
# mismatch + complete subscription snapshot observed. Requires:
# omatorrent-service running (default socket), quickshell (qs).
set -euo pipefail

SOCK="${OT_SOCKET:-${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/omatorrent/service.sock}"
DIR="$(mktemp -d /tmp/ot-qs-test.XXXXXX)"
trap 'rm -rf "$DIR"' EXIT

cat > "$DIR/shell.qml" <<'EOF'
import QtQuick
import Quickshell
import Quickshell.Io

ShellRoot {
  id: root
  property bool helloSent: false
  property int pendingId: -1
  property int nextId: 1
  property int matched: 0
  property int mismatchedIgnored: 0
  property int snapshotBegin: 0
  property int snapshotItems: 0
  property int snapshotEnd: 0

  function send(msg) { sock.write(JSON.stringify(msg) + "\n"); sock.flush() }

  function handle(line) {
    let msg
    try { msg = JSON.parse(line) } catch (e) { return }
    if (!msg || typeof msg.type !== "string") return
    switch (msg.type) {
      case "hello":
        requestStatus()
        subscribe()
        break
      case "system.status":
        if (typeof msg.id !== "number" || msg.id !== pendingId) {
          mismatchedIgnored++
          print("OTQS-IGNORED id " + msg.id)
          return
        }
        pendingId = -1
        matched++
        print("OTQS-READ: " + line)
        if (matched >= 2 && mismatchedIgnored >= 1 && snapshotEnd === 1) {
          print("OTQS-DONE")
          Qt.quit()
        }
        break
      case "torrent.snapshot.begin":
        snapshotBegin++
        if (typeof msg.count !== "number" || typeof msg.id !== "number") {
          print("OTQS-FAIL bad snapshot.begin")
          Qt.quit()
        }
        break
      case "torrent.snapshot.item":
        snapshotItems++
        if (!msg.torrent || typeof msg.torrent.hash !== "string") {
          print("OTQS-FAIL bad snapshot.item")
          Qt.quit()
        }
        break
      case "torrent.snapshot.end":
        snapshotEnd++
        print("OTQS-SNAPSHOT items=" + snapshotItems)
        if (matched >= 2 && mismatchedIgnored >= 1 && snapshotBegin === 1) {
          print("OTQS-DONE")
          Qt.quit()
        }
        break
      case "torrent.subscribed":
      case "torrent.delta":
        break
    }
  }

  function subscribe() {
    send({ type: "torrent.subscribe", id: nextId++ })
    print("OTQS-SENT subscribe")
  }

  function requestStatus() {
    if (pendingId !== -1) return // one in flight
    pendingId = nextId++
    send({ type: "system.status", id: pendingId })
    print("OTQS-SENT status " + pendingId)
    if (pendingId === 1) {
      // While request id 1 is in flight, inject a second request for a
      // different id; its response must be ignored by the matcher above.
      send({ type: "system.status", id: 99 })
      print("OTQS-SENT status 99 (in-flight violation probe)")
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
      if (!root.helloSent && sock.connected) {
        root.helloSent = true
        root.send({ type: "hello", protocol: 1 })
        print("OTQS-SENT hello")
      } else if (root.helloSent && root.pendingId === -1) {
        root.requestStatus()
      }
    }
  }
  Timer { interval: 20000; running: true; onTriggered: Qt.quit() } // hard timeout
}
EOF
sed -i "s|SOCKPATH|${SOCK}|g" "$DIR/shell.qml"

echo "== isolated quickshell IPC smoke against $SOCK =="
timeout 25 qs -p "$DIR" 2>&1 | grep -E "OTQS-" | tee "$DIR/out.log" >&2

hellos=$(grep -c 'OTQS-READ: {"type":"hello"' "$DIR/out.log" || true)
status=$(grep -c 'OTQS-READ: {"type":"system.status"' "$DIR/out.log" || true)
ignored=$(grep -c 'OTQS-IGNORED' "$DIR/out.log" || true)
snap=$(grep -c 'OTQS-SNAPSHOT' "$DIR/out.log" || true)
fail=$(grep -c 'OTQS-FAIL' "$DIR/out.log" || true)
doneMarker=$(grep -c 'OTQS-DONE' "$DIR/out.log" || true)

echo "hello-responses=$hellos matched-status=$status mismatched-ignored=$ignored snapshots=$snap failures=$fail"
if [ "$fail" -eq 0 ] && [ "$doneMarker" -ge 1 ]; then
  echo "PASS: id-matched exchanges + mismatch rejection + full v1.1 subscription snapshot observed"
  exit 0
fi
echo "FAIL: expected no OTQS-FAIL and an OTQS-DONE (hello + >=2 id-matched responses + >=1 ignored mismatch + complete snapshot)"
exit 1
