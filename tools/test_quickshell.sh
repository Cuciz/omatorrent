#!/usr/bin/env bash
# Isolated Quickshell IPC smoke test: proves the Quickshell.Io Socket +
# SplitParser client path against the running omatorrent-service daemon,
# without touching the user's shell.
#
# Exercises the same client discipline as plugins/local.omatorrent
# (docs/IPC.md): exactly one request in flight, responses matched by id,
# mismatched ids ignored.
#
# Exit 0 = handshake + >=2 id-matched status exchanges observed. Requires:
# omatorrent-service running (default socket), quickshell (qs), jq.
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

  function send(msg) { sock.write(JSON.stringify(msg) + "\n"); sock.flush() }

  function handle(line) {
    let msg
    try { msg = JSON.parse(line) } catch (e) { return }
    if (!msg || typeof msg.type !== "string") return
    switch (msg.type) {
      case "hello":
        print("OTQS-READ: " + line)
        requestStatus()
        break
      case "system.status":
        // Same matching rule as the widget: act only on the pending id.
        if (typeof msg.id !== "number" || msg.id !== pendingId) {
          mismatchedIgnored++
          print("OTQS-IGNORED id " + msg.id)
          return
        }
        pendingId = -1
        matched++
        print("OTQS-READ: " + line)
        break
    }
  }

  function requestStatus() {
    if (pendingId !== -1) return // one in flight
    pendingId = nextId++
    send({ type: "system.status", id: pendingId })
    print("OTQS-SENT status " + pendingId)
    if (pendingId === 2) {
      // While request id 2 is in flight, inject a second request for a
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
        if (root.matched >= 3 && root.mismatchedIgnored >= 1) Qt.quit()
      }
    }
  }
  Timer { interval: 15000; running: true; onTriggered: Qt.quit() } // hard timeout
}
EOF
sed -i "s|SOCKPATH|${SOCK}|g" "$DIR/shell.qml"

echo "== isolated quickshell IPC smoke against $SOCK =="
timeout 20 qs -p "$DIR" 2>&1 | grep -E "OTQS-" | tee "$DIR/out.log" >&2

hellos=$(grep -c 'OTQS-READ: {"type":"hello"' "$DIR/out.log" || true)
status=$(grep -c 'OTQS-READ: {"type":"system.status"' "$DIR/out.log" || true)
ok=$(grep -c '"qbittorrent":"ok"' "$DIR/out.log" || true)
unavail=$(grep -c '"qbittorrent":"unavailable"' "$DIR/out.log" || true)
ignored=$(grep -c 'OTQS-IGNORED' "$DIR/out.log" || true)

echo "hello-responses=$hellos matched-status=$status ok=$ok unavailable=$unavail mismatched-ignored=$ignored"
if [ "$hellos" -ge 1 ] && [ "$status" -ge 3 ] && [ "$ignored" -ge 1 ] && { [ "$ok" -ge 3 ] || [ "$unavail" -ge 3 ]; }; then
  echo "PASS: handshake + id-matched status exchanges + mismatched-id rejection observed"
  exit 0
fi
echo "FAIL: expected hello + >=3 id-matched responses + >=1 ignored mismatch"
exit 1
