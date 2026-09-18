#!/usr/bin/env bash
# Isolated Quickshell IPC smoke test: proves the Quickshell.Io Socket +
# SplitParser client path against the running omatorrent-service daemon,
# without touching the user's shell.
#
# Exit 0 = full exchange observed (hello + >=2 system.status responses
# with real data). Requires: omatorrent-service running (default socket),
# quickshell (qs) installed, jq.
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
  property int n: 0
  function send(msg) { sock.write(JSON.stringify(msg) + "\n"); sock.flush() }
  Socket {
    id: sock
    path: "SOCKPATH"
    connected: true
    parser: SplitParser {
      splitMarker: "\n"
      onRead: function (d) { print("OTQS-READ: " + d) }
    }
    onError: function (e) { print("OTQS-ERROR " + e) }
  }
  Timer {
    interval: 500
    running: true
    repeat: true
    onTriggered: {
      if (!root.helloSent && sock.connected) {
        root.helloSent = true
        root.send({ type: "hello", protocol: 1 })
        print("OTQS-SENT hello")
      } else if (root.helloSent) {
        root.n++
        root.send({ type: "system.status", id: root.n })
        print("OTQS-SENT status " + root.n)
        if (root.n >= 3) Qt.quit()
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

echo "hello-responses=$hellos status-responses=$status ok=$ok unavailable=$unavail"
if [ "$hellos" -ge 1 ] && [ "$status" -ge 2 ] && { [ "$ok" -ge 2 ] || [ "$unavail" -ge 2 ]; }; then
  echo "PASS: handshake + status exchange observed"
  exit 0
fi
echo "FAIL: expected hello + >=2 status responses"
exit 1
