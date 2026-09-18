import QtQuick
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui

// OmaTorrent bar widget: presentation only (ADR-0001). All state comes
// from omatorrent-service over IPC v1 (ADR-0004/0005) via a Unix socket;
// no qBittorrent HTTP, no secrets, no business logic here.
//
// Client discipline (docs/IPC.md): exactly one request in flight per
// connection; responses are matched by id; a response with an
// unexpected id is ignored; pending state resets on disconnect.
//
// Clicking opens the torrent panel (the native Omarchy popout pattern,
// same as the first-party clock/moon-phase widgets).
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

  // One-in-flight tracking: id of the outstanding system.status request,
  // -1 when none. pendingSince guards against a daemon that accepts the
  // frame but never answers (reconnect after ~3 poll intervals).
  property int pendingId: -1
  property real pendingSince: 0

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
      pendingId = -1
      send({ type: "hello", protocol: 1 })
    }
  }

  function requestStatus() {
    if (pendingId !== -1) return // exactly one request in flight
    pendingId = nextId++
    pendingSince = Date.now()
    send({ type: "system.status", id: pendingId })
  }

  function resetSession() {
    helloSent = false
    pendingId = -1
    backendOk = false
    dlSpeed = 0
    upSpeed = 0
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
        requestStatus()
        break
      case "system.status":
        // Match by id; anything else (stale/duplicate/foreign) is ignored.
        if (typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
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

  // ---- Torrent panel (native popout; shape contract for
  //      shell.summon/hide/toggle routing).
  readonly property bool opened: panelLoader.item ? panelLoader.item.opened === true : false

  function open() {
    if (panelLoader.item) panelLoader.item.open()
  }
  function close() {
    if (panelLoader.item) panelLoader.item.close()
  }
  function toggle() {
    if (panelLoader.item) panelLoader.item.toggle()
  }

  function injectPanel() {
    var target = panelLoader.item
    if (!target) return
    if ("bar" in target) target.bar = root.bar
    if ("anchorItem" in target) target.anchorItem = button
    if ("hostWidget" in target) target.hostWidget = root
  }

  implicitWidth: button.implicitWidth
  implicitHeight: button.implicitHeight

  onBarChanged: injectPanel()

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
        root.resetSession()
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
      if (!root.helloSent) return
      if (root.pendingId !== -1) {
        // Still waiting for the outstanding response. The daemon answers
        // or closes within its 5s write deadline; past that the session
        // is dead — drop it and let the reconnect path take over.
        if (Date.now() - root.pendingSince > 6000) {
          root.resetSession()
          sock.connected = false
        }
        return
      }
      root.requestStatus()
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

  Loader {
    id: panelLoader
    active: true
    source: Qt.resolvedUrl("Panel.qml")
    visible: false
    onLoaded: {
      root.injectPanel()
      Qt.callLater(root.injectPanel)
    }
  }

  WidgetButton {
    id: button
    anchors.fill: parent
    bar: root.bar
    text: root.statusText
    foreground: root.statusColor
    labelVisible: true
    hasVisualContent: true
    tooltipText: root.tooltip
    onPressed: root.toggle()

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
