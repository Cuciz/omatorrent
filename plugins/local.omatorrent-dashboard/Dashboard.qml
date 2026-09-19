import QtQuick
import QtQuick.Controls
import Quickshell
import Quickshell.Io
import Quickshell.Wayland
import qs.Commons
import qs.Ui

// OmaTorrent dashboard overlay (Phase 0.4, ADR-0007): a large native
// Omarchy overlay card rendering CURRENT daemon state only. All numbers
// come from the daemon's v1.3 dashboard.status aggregates over the same
// IPC discipline as the bar widget/panel (one request in flight,
// id-matched, cache-served — answering never contacts qBittorrent).
// Presentation only (ADR-0001): no torrent-list mirror, no qBittorrent
// vocabulary, no history — formatting bytes/percentages is the extent
// of local computation.
//
// Lifecycle: an overlay-kind plugin (the omarchy.menu model) summoned
// via `omarchy-shell shell toggle local.omatorrent-dashboard` or the
// torrent panel's header button. Not keepLoaded: the shell's Loader is
// active ONLY while summoned — closing destroys this component and its
// socket, so a hidden dashboard costs nothing. A user-initiated close
// MUST call shell.hide() so the host's open state stays consistent
// (first-party Emojis.dismiss rule). Window skeleton follows the
// first-party overlay pattern (emojis/clipboard): full-anchor
// layer-shell panel, scrim, centered card, Escape/click-outside close,
// keyboard focus while open.
Item {
  id: root

  // Injected by the shell host on load.
  property var shell: null
  property var manifest: null

  readonly property string pluginId: (manifest && manifest.id) || "local.omatorrent-dashboard"

  // ---- v1.3 frame state (one shape at a time).
  property bool opened: false
  property bool daemonUp: false
  property bool live: false               // qbittorrent:"ok" frame seen
  property bool haveLastKnown: false      // degraded frame with last_known
  property string appVersion: ""
  property string webapiVersion: ""
  property real dlSpeed: 0
  property real upSpeed: 0
  property var freeSpace: undefined       // undefined = backend never reported
  property var counts: ({})
  property var aggregate: ({})
  property var active: []
  property int nextId: 1
  property int pendingId: -1
  property real pendingSince: 0
  property int backoffMs: 1000
  property bool helloSent: false

  readonly property string xdgRuntime: Quickshell.env("XDG_RUNTIME_DIR") || ""
  readonly property string socketPath: xdgRuntime !== "" ? xdgRuntime + "/omatorrent/service.sock" : ""

  // Host contract: the shell calls open(payload) once loaded, and
  // close() when hiding from the host side (toggle CLI, summon of
  // another surface). close must NOT call back into shell.hide — the
  // shell is already hiding this plugin and re-entry would recurse
  // (first-party Emojis.close/dismiss split).
  function open(payloadJson) {
    root.opened = true
    Qt.callLater(function () { keyCatcher.forceActiveFocus() })
  }
  function close() {
    root.opened = false
  }
  // User-initiated close (Escape, click on the scrim): notifies the
  // host so its open state stays consistent.
  function dismiss() {
    root.opened = false
    if (shell && typeof shell.hide === "function") shell.hide(pluginId)
  }

  function send(msg) {
    if (!sock.connected) return
    sock.write(JSON.stringify(msg) + "\n")
    sock.flush()
  }

  function ensureSession() {
    if (sock.connected && !helloSent) {
      helloSent = true
      pendingId = -1
      send({ type: "hello", protocol: 1 })
    }
  }

  function requestDashboard() {
    if (pendingId !== -1) return
    pendingId = nextId++
    pendingSince = Date.now()
    send({ type: "dashboard.status", id: pendingId })
  }

  function resetSession() {
    helloSent = false
    pendingId = -1
    daemonUp = false
    live = false
    haveLastKnown = false
    dlSpeed = 0
    upSpeed = 0
    freeSpace = undefined
    active = []
  }

  function handleLine(line) {
    if (line.length > 4096) return
    let msg
    try {
      msg = JSON.parse(line)
    } catch (e) {
      return
    }
    if (!msg || typeof msg.type !== "string") return
    switch (msg.type) {
      case "hello":
        requestDashboard()
        break
      case "dashboard.status":
        if (typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
        daemonUp = true
        if (msg.qbittorrent === "ok") {
          live = true
          haveLastKnown = false
          appVersion = typeof msg.app_version === "string" ? msg.app_version : ""
          webapiVersion = typeof msg.webapi_version === "string" ? msg.webapi_version : ""
          dlSpeed = typeof msg.dl_speed === "number" ? msg.dl_speed : 0
          upSpeed = typeof msg.up_speed === "number" ? msg.up_speed : 0
          freeSpace = typeof msg.free_space === "number" ? msg.free_space : undefined
          counts = msg.counts && typeof msg.counts === "object" ? msg.counts : ({})
          aggregate = msg.aggregate && typeof msg.aggregate === "object" ? msg.aggregate : ({})
          active = Array.isArray(msg.active) ? msg.active : []
        } else {
          // Degraded: speeds/active are unknown — never fabricated.
          live = false
          dlSpeed = 0
          upSpeed = 0
          freeSpace = undefined
          active = []
          if (msg.last_known && typeof msg.last_known === "object") {
            haveLastKnown = true
            appVersion = typeof msg.last_known.app_version === "string" ? msg.last_known.app_version : ""
            webapiVersion = typeof msg.last_known.webapi_version === "string" ? msg.last_known.webapi_version : ""
            counts = msg.last_known.counts && typeof msg.last_known.counts === "object" ? msg.last_known.counts : ({})
            aggregate = msg.last_known.aggregate && typeof msg.last_known.aggregate === "object" ? msg.last_known.aggregate : ({})
          } else {
            haveLastKnown = false
            counts = ({})
            aggregate = ({})
          }
        }
        break
      case "error":
        break
    }
  }

  // ---- Presentation formatting (the only local computation).
  function formatSpeed(bps) {
    if (bps >= 1048576) return (bps / 1048576).toFixed(1) + " MB/s"
    if (bps >= 1024) return (bps / 1024).toFixed(0) + " kB/s"
    return Math.round(bps) + " B/s"
  }

  function formatBytes(b) {
    if (typeof b !== "number" || !isFinite(b)) return "—"
    if (b >= 1099511627776) return (b / 1099511627776).toFixed(1) + " TB"
    if (b >= 1073741824) return (b / 1073741824).toFixed(1) + " GB"
    if (b >= 1048576) return (b / 1048576).toFixed(0) + " MB"
    if (b >= 1024) return (b / 1024).toFixed(0) + " kB"
    return Math.round(b) + " B"
  }

  function countOf(key) {
    const v = counts[key]
    return typeof v === "number" && isFinite(v) ? v : 0
  }

  function bytesOf(key) {
    const v = aggregate[key]
    return typeof v === "number" && isFinite(v) ? v : undefined
  }

  readonly property real overallProgress: {
    const total = bytesOf("total_size")
    const done = bytesOf("completed_bytes")
    return total > 0 ? Math.min(1, Math.max(0, done / total)) : 0
  }

  readonly property color foreground: Color.menu.text
  readonly property color dim: Qt.darker(Color.menu.text, 1.55)
  readonly property color urgent: Color.urgent
  readonly property var borderSpec: Border.surfaceSpec("menu", "border", Color.menu.border, Math.max(1, Style.space(2)))

  readonly property string headerState: !daemonUp
    ? "daemon offline"
    : (live ? "connected" : (haveLastKnown ? "qBittorrent unreachable" : "waiting for first sync"))

  // Versions are provenance of the last live frame; while the daemon
  // is offline nothing may read as current knowledge.
  readonly property string versionText: daemonUp && appVersion !== ""
    ? "qBittorrent " + appVersion + (webapiVersion !== "" ? " · WebAPI " + webapiVersion : "")
    : ""

  // Label + right-aligned value row (agents-panel metric idiom).
  component MetricRow: Item {
    id: row
    property string label
    property string value
    property bool urgentValue: false
    property bool dimValue: false
    width: parent ? parent.width : 0
    implicitHeight: Math.max(labelText.implicitHeight, valueText.implicitHeight)

    Text {
      id: labelText
      anchors.left: parent.left
      anchors.verticalCenter: parent.verticalCenter
      text: row.label
      color: root.foreground
      opacity: 0.85
      font.pixelSize: Style.font.body
      font.family: Style.font.family
    }
    Text {
      id: valueText
      anchors.right: parent.right
      anchors.verticalCenter: parent.verticalCenter
      text: row.value
      color: row.urgentValue ? root.urgent : (row.dimValue ? root.dim : Color.accent)
      font.pixelSize: Style.font.body
      font.family: Style.font.family
    }
  }

  PanelWindow {
    id: panel
    visible: root.opened
    anchors { top: true; bottom: true; left: true; right: true }
    color: "transparent"
    WlrLayershell.namespace: "omatorrent-dashboard"
    WlrLayershell.layer: WlrLayer.Overlay
    WlrLayershell.keyboardFocus: root.opened ? WlrKeyboardFocus.Exclusive : WlrKeyboardFocus.None
    exclusionMode: ExclusionMode.Ignore

    Rectangle {
      anchors.fill: parent
      color: Color.menu.scrim
      MouseArea {
        anchors.fill: parent
        onClicked: root.dismiss()
      }
    }

    BorderSurface {
      id: card
      width: Math.min(Style.space(500), panel.width - Style.gapsOut * 2)
      height: Math.min(content.implicitHeight + Style.spacing.panelPadding * 2,
                       panel.height - Style.gapsOut * 2)
      radius: Style.cornerRadius
      anchors.centerIn: parent
      color: Color.menu.background
      borderSpec: root.borderSpec
      padding: Style.spacing.panelPadding
      MouseArea { anchors.fill: parent; onClicked: {} } // swallow card clicks

      Item {
        id: keyCatcher
        anchors.fill: parent
        focus: true
        Keys.priority: Keys.BeforeItem
        Keys.onPressed: function (event) {
          if (event.key === Qt.Key_Escape) {
            root.dismiss()
            event.accepted = true
          }
        }

        Flickable {
          id: flick
          anchors.fill: parent
          clip: true
          boundsBehavior: Flickable.StopAtBounds
          flickableDirection: Flickable.VerticalFlick
          interactive: contentHeight > height
          contentWidth: width

          ScrollBar.vertical: ScrollBar { policy: ScrollBar.AsNeeded }

          Column {
            id: content
            width: flick.width
            spacing: Style.space(12)

            // ---- Header: title, live state, versions.
            Item {
              width: parent.width
              implicitHeight: Math.max(titleText.implicitHeight, stateText.implicitHeight, versionTextItem.implicitHeight) + Style.space(1)

              Rectangle {
                width: Style.space(3)
                height: Style.space(3)
                radius: width / 2
                anchors.left: parent.left
                anchors.verticalCenter: titleText.verticalCenter
                color: root.live ? Color.accent : root.urgent
              }
              Text {
                id: titleText
                text: "OmaTorrent"
                anchors.left: parent.left
                anchors.leftMargin: Style.space(5)
                anchors.top: parent.top
                color: root.foreground
                font.pixelSize: Style.font.title
                font.family: Style.font.family
              }
              Text {
                id: stateText
                text: root.headerState
                anchors.right: parent.right
                anchors.top: parent.top
                color: root.live ? root.foreground : root.urgent
                opacity: 0.75
                font.pixelSize: Style.font.caption
                font.family: Style.font.family
              }
              Text {
                id: versionTextItem
                text: root.versionText
                anchors.right: parent.right
                anchors.top: stateText.bottom
                anchors.topMargin: Style.space(1)
                color: root.dim
                font.pixelSize: Style.font.caption
                font.family: Style.font.family
              }
            }

            // ---- Degraded callouts (agents-style urgent tint).
            BorderSurface {
              visible: !root.daemonUp
              width: parent.width
              implicitHeight: offlineText.implicitHeight + Style.space(8)
              radius: Style.cornerRadius
              color: Util.alpha(root.urgent, 0.10)
              borderSpec: Border.flat(Util.alpha(root.urgent, 0.35), 1)

              Text {
                id: offlineText
                anchors.centerIn: parent
                width: parent.width - Style.space(12)
                text: "omatorrent-service offline"
                color: root.urgent
                font.pixelSize: Style.font.body
                font.family: Style.font.family
                horizontalAlignment: Text.AlignHCenter
              }
            }
            BorderSurface {
              visible: root.daemonUp && !root.live
              width: parent.width
              implicitHeight: degradedText.implicitHeight + Style.space(8)
              radius: Style.cornerRadius
              color: Util.alpha(root.urgent, 0.10)
              borderSpec: Border.flat(Util.alpha(root.urgent, 0.35), 1)

              Text {
                id: degradedText
                anchors.centerIn: parent
                width: parent.width - Style.space(12)
                text: root.haveLastKnown
                  ? "qBittorrent unreachable — showing last known state"
                  : "qBittorrent unreachable — waiting for first sync"
                color: root.urgent
                font.pixelSize: Style.font.body
                font.family: Style.font.family
                horizontalAlignment: Text.AlignHCenter
                wrapMode: Text.WordWrap
              }
            }

            // ---- Live transfer: hidden while degraded (unknown is not
            //      zero; the daemon omits these fields for that reason).
            Column {
              visible: root.live
              width: parent.width
              spacing: Style.space(6)

              PanelSectionHeader { text: "Live transfer" }
              MetricRow { label: "\u2193  Download"; value: root.formatSpeed(root.dlSpeed) }
              MetricRow { label: "\u2191  Upload"; value: root.formatSpeed(root.upSpeed) }
              Text {
                width: parent.width
                text: countOf("active") + (countOf("active") === 1 ? " torrent transferring now" : " torrents transferring now")
                color: root.dim
                font.pixelSize: Style.font.caption
                font.family: Style.font.family
              }
            }

            // ---- Torrent population (last-known while degraded).
            Column {
              visible: root.live || root.haveLastKnown
              width: parent.width
              spacing: Style.space(6)

              PanelSectionHeader { text: "Torrents" }
              MetricRow { dimValue: !root.live; label: "Total"; value: String(countOf("total")) }
              MetricRow { dimValue: !root.live; label: "Downloading"; value: String(countOf("downloading")) }
              MetricRow { dimValue: !root.live; label: "Seeding"; value: String(countOf("seeding")) }
              MetricRow { dimValue: !root.live; label: "Paused"; value: String(countOf("paused")) }
              MetricRow { dimValue: !root.live; label: "Completed"; value: String(countOf("completed")) }
            }

            // ---- Current data (last-known while degraded).
            Column {
              visible: root.live || root.haveLastKnown
              width: parent.width
              spacing: Style.space(6)

              PanelSectionHeader { text: "Current data" }
              MetricRow { dimValue: !root.live; label: "Total size"; value: formatBytes(bytesOf("total_size")) }
              MetricRow { dimValue: !root.live; label: "Downloaded"; value: formatBytes(bytesOf("completed_bytes")) }
              MetricRow { dimValue: !root.live; label: "Remaining"; value: formatBytes(bytesOf("remaining_bytes")) }
              MetricRow {
                visible: root.live && root.freeSpace !== undefined
                label: "Free space (default save path)"
                value: formatBytes(root.freeSpace)
              }

              // Overall completion track (same native track style as
              // the panel rows; informational, not decoration).
              Item {
                visible: root.live || root.haveLastKnown
                width: parent.width
                height: Math.max(2, Style.space(0.75))
                Rectangle {
                  anchors.fill: parent
                  radius: height / 2
                  color: Color.muted
                  opacity: 0.25
                }
                Rectangle {
                  width: parent.width * root.overallProgress
                  height: parent.height
                  radius: parent.height / 2
                  color: root.overallProgress >= 1 ? Color.muted : Color.accent
                  opacity: root.live ? 1.0 : 0.5
                }
              }
              Text {
                visible: root.live || root.haveLastKnown
                width: parent.width
                text: (root.live ? "" : "last known — ") + Math.round(root.overallProgress * 100) + "% of all data downloaded"
                color: root.dim
                font.pixelSize: Style.font.caption
                font.family: Style.font.family
              }
            }

            // ---- Transferring now (current state; never history).
            Column {
              visible: root.live
              width: parent.width
              spacing: Style.space(4)

              PanelSectionHeader { text: "Transferring now" }

              Repeater {
                model: root.active

                Item {
                  width: content.width
                  implicitHeight: Math.max(activeName.implicitHeight, activeSpeeds.implicitHeight)
                  required property var modelData

                  Text {
                    id: activeName
                    anchors.left: parent.left
                    anchors.verticalCenter: parent.verticalCenter
                    width: parent.width - activeSpeeds.implicitWidth - Style.space(6)
                    text: modelData && typeof modelData.name === "string" ? modelData.name : ""
                    color: root.foreground
                    opacity: 0.9
                    font.pixelSize: Style.font.bodySmall
                    font.family: Style.font.family
                    elide: Text.ElideRight
                  }
                  Text {
                    id: activeSpeeds
                    anchors.right: parent.right
                    anchors.verticalCenter: parent.verticalCenter
                    text: {
                      if (!modelData) return ""
                      const dl = typeof modelData.dlspeed === "number" ? modelData.dlspeed : 0
                      const up = typeof modelData.upspeed === "number" ? modelData.upspeed : 0
                      return (dl > 0 ? "\u2193 " + root.formatSpeed(dl) + "  " : "") +
                             (up > 0 ? "\u2191 " + root.formatSpeed(up) : "")
                    }
                    color: Color.accent
                    font.pixelSize: Style.font.bodySmall
                    font.family: Style.font.family
                  }
                }
              }

              Text {
                visible: root.active.length === 0
                width: parent.width
                text: "Nothing transferring"
                color: root.dim
                font.pixelSize: Style.font.bodySmall
                font.family: Style.font.family
              }
            }

            // ---- Navigation: to the torrent panel (mutations stay in
            //      the panel; the dashboard does not duplicate them).
            PanelSeparator { }

            Row {
              width: parent.width
              spacing: Style.space(4)

              Text {
                text: "Open torrent panel"
                color: Color.accent
                opacity: 0.9
                font.pixelSize: Style.font.body
                font.family: Style.font.family
                MouseArea {
                  anchors.fill: parent
                  cursorShape: Qt.PointingHandCursor
                  onClicked: {
                    // First-party routing (menu model): dismiss this
                    // overlay, then summon the bar-widget panel via the
                    // documented host CLI — the capability-scoped shell
                    // API a plugin receives cannot summon OTHER plugins
                    // (PluginShellApi gate), but the CLI routes through
                    // the host itself.
                    root.dismiss()
                    Util.execDetached("omarchy-shell shell summon local.omatorrent")
                  }
                }
              }
              Item { width: parent.width - openPanelText.implicitWidth - escText.implicitWidth - Style.space(16); height: 1 }
              Text {
                id: escText
                anchors.verticalCenter: parent.verticalCenter
                text: "Esc to close"
                color: root.dim
                font.pixelSize: Style.font.caption
                font.family: Style.font.family
              }
            }
            Text {
              id: openPanelText
              visible: false
              text: "Open torrent panel"
              font.pixelSize: Style.font.body
              font.family: Style.font.family
            }
          }
        }
      }
    }
  }

  // ---- v1.3 client: alive only while this component is loaded (i.e.
  //      while the dashboard is open). Closing destroys it.
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
        root.backoffMs = 1000
        root.ensureSession()
      } else {
        root.resetSession()
      }
    }
  }

  Timer {
    interval: 2000
    running: true
    repeat: true
    onTriggered: {
      root.ensureSession()
      if (!root.helloSent) return
      if (root.pendingId !== -1) {
        if (Date.now() - root.pendingSince > 6000) {
          root.resetSession()
          sock.connected = false
        }
        return
      }
      root.requestDashboard()
    }
  }

  Timer {
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
}
