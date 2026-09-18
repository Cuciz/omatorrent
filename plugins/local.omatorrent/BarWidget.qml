import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui

// OmaTorrent Phase 0 bar proof: presentation only (ADR-0001). All state
// comes from omatorrent-service over IPC v1 (ADR-0004) via a Unix socket;
// no qBittorrent HTTP, no secrets, no business logic here.
BarWidget {
  id: root
  moduleName: "local.omatorrent"

  // Visible states: "daemon-off" | "backend-off" | "idle" | "active"
  readonly property string visibleState: {
    if (!connectedOnce) return "daemon-off"
    if (!sock.connected) return "daemon-off"
    if (backendOk !== true) return "backend-off"
    return (dlSpeed > 0 || upSpeed > 0) ? "active" : "idle"
  }

  property bool connectedOnce: false
  property bool backendOk: false
  property real dlSpeed: 0
  property real upSpeed: 0
  property string appVersion: ""
  property string webapiVersion: ""
  property int torrentsTotal: 0
  property int nextId: 1
  property int backoffMs: 1000

  readonly property string xdgRuntime: Quickshell.env("XDG_RUNTIME_DIR") || ""
  readonly property string socketPath: xdgRuntime !== "" ? xdgRuntime + "/omatorrent/service.sock" : ""

  property bool helloSent: false

  function send(msg) {
    if (!sock.connected) return
    sock.write(JSON.stringify(msg) + "\n")
    sock.flush()
  }

  // The initial `connected: true` does not fire connectionStateChanged
  // (QML construction ordering), so the hello is bootstrap-aware instead
  // of relying on the signal alone.
  function ensureSession() {
    if (sock.connected && !helloSent) {
      helloSent = true
      send({ type: "hello", protocol: 1 })
    }
  }

  function requestStatus() {
    send({ type: "system.status", id: nextId++ })
  }

  function handleLine(line) {
    if (line.length > 4096) return // daemon frames are bounded; be robust anyway
    let msg
    try {
      msg = JSON.parse(line)
    } catch (e) {
      return // never crash the shell on a bad frame
    }
    if (!msg || typeof msg.type !== "string") return
    switch (msg.type) {
      case "hello":
        // system.status already carries backend reachability; no separate
        // health request is needed for the proof.
        requestStatus()
        break
      case "health":
        backendOk = msg.backend === "ok"
        break
      case "system.status":
        if (msg.qbittorrent === "ok") {
          backendOk = true
          appVersion = msg.app_version || ""
          webapiVersion = msg.webapi_version || ""
          torrentsTotal = msg.torrents_total || 0
          dlSpeed = msg.dl_speed || 0
          upSpeed = msg.up_speed || 0
        } else {
          backendOk = false
          dlSpeed = 0
          upSpeed = 0
        }
        break
      case "error":
        // Protocol error: the server closes; reconnect via state change.
        break
    }
  }

  function formatSpeed(bps) {
    if (bps >= 1048576) return (bps / 1048576).toFixed(1) + " MB/s"
    if (bps >= 1024) return (bps / 1024).toFixed(0) + " kB/s"
    return Math.round(bps) + " B/s"
  }

  readonly property string statusText: {
    switch (visibleState) {
      case "daemon-off": return "qBT OFFLINE"
      case "backend-off": return "qBT ERROR"
      case "idle": return "qBT \u25CF"
      default: return "\u2193 " + formatSpeed(dlSpeed) + "  \u2191 " + formatSpeed(upSpeed)
    }
  }

  readonly property color statusColor: {
    switch (visibleState) {
      case "daemon-off": return Color.muted
      case "backend-off": return bar ? bar.urgent : Color.urgent
      case "idle": return Color.muted
      default: return bar ? bar.barForeground : Color.foreground
    }
  }

  readonly property string tooltip: {
    switch (visibleState) {
      case "daemon-off": return "omatorrent-service is not running"
      case "backend-off": return "qBittorrent unreachable"
      default: return "qBittorrent " + appVersion + " (WebAPI " + webapiVersion + ") — " + torrentsTotal + " torrents"
    }
  }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  Socket {
    id: sock
    path: root.socketPath
    connected: false

    parser: SplitParser {
      splitMarker: "\n"
      onRead: function (data) { root.handleLine(data) }
    }

    onConnectionStateChanged: {
      if (sock.connected) {
        root.connectedOnce = true
        root.backoffMs = 1000
        root.ensureSession()
      } else {
        root.helloSent = false
        root.backendOk = false
        root.dlSpeed = 0
        root.upSpeed = 0
      }
    }
  }

  Timer {
    id: pollTimer
    interval: 2000
    running: true
    repeat: true
    onTriggered: {
      root.ensureSession()
      if (root.helloSent) root.requestStatus()
    }
  }

  // Self-healing reconnect: checked on every tick regardless of socket
  // signals, so a missed state change cannot strand the widget offline.
  Timer {
    id: reconnectTimer
    interval: root.backoffMs
    running: true
    repeat: true
    onTriggered: {
      if (!root.socketPath) return
      if (sock.connected) {
        root.backoffMs = 1000
      } else {
        sock.connected = true
        root.backoffMs = Math.min(root.backoffMs * 2, 10000)
      }
    }
  }

  Component.onCompleted: if (root.socketPath !== "") sock.connected = true

  WidgetButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: root.statusText
    foreground: root.statusColor
    labelVisible: true
    hasVisualContent: true
    tooltipText: root.tooltip
    implicitWidth: vertical ? barSize : textMetrics.implicitWidth + scaledHorizontalMargin * 2
    implicitHeight: vertical ? barSize : Math.max(barSize, textMetrics.implicitHeight + scaledVerticalPadding * 2)

    Text {
      id: textMetrics
      visible: false
      font.family: button.fontFamily
      font.pixelSize: button.fontSize
      text: root.statusText
    }
  }
}
