import QtQuick
import QtQuick.Controls
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui

// OmaTorrent torrent panel: presentation only (ADR-0001). Renders the
// daemon's normalized torrent state over IPC v1.1 (ADR-0005): one
// subscription delivers a full snapshot then bounded delta frames; the
// header polls v1.0 system.status for backend state and global speeds.
// No qBittorrent API knowledge here — hashes arrive from the daemon and
// merge semantics are the daemon's.
Panel {
  id: root
  moduleName: "local.omatorrent"
  manageIpc: false

  property var anchorItem: null
  property var hostWidget: null

  // ---- Header state (v1.0 system.status poll, same contract as the
  //      bar widget: one in flight, id-matched).
  property bool daemonUp: false
  property bool backendOk: false
  property real dlSpeed: 0
  property real upSpeed: 0
  property int nextId: 1
  property int pendingId: -1
  property real pendingSince: 0
  property int backoffMs: 1000
  property bool helloSent: false

  // ---- Torrent state (v1.1 subscription).
  property var torrents: ({})        // hash -> item object
  property var order: []             // hashes, sorted by name
  property var rowIndex: ({})        // hash -> visible row (valid between rebuilds)
  property string filter: "all"
  property bool subscribed: false
  property bool applyingSnapshot: false

  readonly property string xdgRuntime: Quickshell.env("XDG_RUNTIME_DIR") || ""
  readonly property string socketPath: xdgRuntime !== "" ? xdgRuntime + "/omatorrent/service.sock" : ""

  readonly property var filters: ["all", "active", "downloading", "seeding", "paused", "completed"]

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

  function requestStatus() {
    if (pendingId !== -1) return
    pendingId = nextId++
    pendingSince = Date.now()
    send({ type: "system.status", id: pendingId })
  }

  function resetSession() {
    helloSent = false
    subscribed = false
    subId = null
    pendingId = -1
    daemonUp = false
    backendOk = false
    dlSpeed = 0
    upSpeed = 0
  }

  function formatSpeed(bps) {
    if (bps >= 1048576) return (bps / 1048576).toFixed(1) + " MB/s"
    if (bps >= 1024) return (bps / 1024).toFixed(0) + " kB/s"
    return Math.round(bps) + " B/s"
  }

  function formatEta(eta) {
    if (eta === undefined || eta >= 8640000) return "\u221E"
    if (eta >= 3600) return Math.floor(eta / 3600) + "h" + String(Math.floor((eta % 3600) / 60)).padStart(2, "0")
    if (eta >= 60) return Math.floor(eta / 60) + "m"
    return eta + "s"
  }

  // ---- Source-of-truth updates; the view model syncs incrementally.
  function clearTorrents() {
    torrents = ({})
    order = []
    view.clear()
  }

  function insertSorted(hash, item) {
    let lo = 0
    let hi = order.length
    while (lo < hi) {
      const mid = (lo + hi) >> 1
      if (torrents[order[mid]].name < item.name) lo = mid + 1
      else hi = mid
    }
    order.splice(lo, 0, hash)
  }

  function validItem(item) {
    return item && typeof item.hash === "string" && item.hash.length > 0 && item.hash.length <= 64
  }

  function applyItem(item) {
    if (!validItem(item)) return // malformed daemon data never reaches the model
    const isNew = torrents[item.hash] === undefined
    torrents[item.hash] = item
    if (isNew) insertSorted(item.hash, item)
    if (applyingSnapshot) return // batched: one rebuild at snapshot end
    syncRow(item.hash, item, isNew)
  }

  function applyRemoved(hash) {
    if (typeof hash !== "string" || torrents[hash] === undefined) return
    delete torrents[hash]
    const i = order.indexOf(hash)
    if (i >= 0) order.splice(i, 1)
    if (!applyingSnapshot) rebuildView()
  }

  // ---- View sync: membership or order change rebuilds the view; a
  //      pure data change updates the row in place (no O(N) churn).
  function matchesFilter(item) {
    switch (filter) {
      case "all": return true
      case "active": return item.dlspeed > 0 || item.upspeed > 0
      case "downloading": return item.state === "downloading"
      case "seeding": return item.state === "seeding"
      case "paused": return item.state === "paused"
      case "completed": return item.progress >= 1
    }
    return true
  }

  function rebuildView() {
    view.clear()
    rowIndex = ({})
    for (let i = 0; i < order.length; i++) {
      const t = torrents[order[i]]
      if (matchesFilter(t)) {
        rowIndex[t.hash] = view.count
        view.append(row(t))
      }
    }
  }

  function viewIndex(hash) {
    const i = rowIndex[hash]
    if (i === undefined || i >= view.count || view.get(i).hash !== hash) return -1
    return i
  }

  function row(t) {
    return {
      hash: t.hash,
      name: t.name,
      state: t.state,
      progress: t.progress,
      dlspeed: t.dlspeed || 0,
      upspeed: t.upspeed || 0,
      eta: t.eta,
      ratio: t.ratio || 0
    }
  }

  function syncRow(hash, item, isNew) {
    if (!isNew && matchesFilter(item)) {
      const i = viewIndex(hash)
      if (i >= 0) {
        view.set(i, row(item))
        return
      }
    }
    rebuildView() // membership or order changed
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
        requestStatus()
        break
      case "system.status":
        if (typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
        daemonUp = true
        backendOk = msg.qbittorrent === "ok"
        dlSpeed = msg.dl_speed || 0
        upSpeed = msg.up_speed || 0
        // Subscribe only after the first status completed: exactly one
        // request in flight at any time (docs/IPC.md client rules).
        if (!subscribed) {
          subscribed = true
          send({ type: "torrent.subscribe", id: nextId++ })
        }
        break
      case "torrent.snapshot.begin":
        applyingSnapshot = true
        clearTorrents()
        break
      case "torrent.snapshot.item":
        if (msg.torrent && typeof msg.torrent.hash === "string") {
          applyItem(msg.torrent)
        }
        break
      case "torrent.delta":
        if (Array.isArray(msg.changed)) {
          for (let i = 0; i < msg.changed.length; i++) applyItem(msg.changed[i])
        }
        if (Array.isArray(msg.removed)) {
          for (let i = 0; i < msg.removed.length; i++) applyRemoved(msg.removed[i])
        }
        break
      case "torrent.subscribed":
      case "error":
        break
      case "torrent.snapshot.end":
        applyingSnapshot = false
        rebuildView() // single O(N) pass for the whole snapshot
        break
    }
  }

  function setFilter(f) {
    if (filter === f) return
    filter = f
    rebuildView()
  }

  readonly property color stateColor: backendOk ? Color.accent : (bar ? bar.urgent : Color.urgent)
  readonly property string headerState: !daemonUp ? "daemon offline" : (backendOk ? "connected" : "qBittorrent unreachable")

  function open() {
    root.controller.show()
  }
  function close() {
    root.controller.hide()
  }

  KeyboardPanel {
    id: panel
    anchorItem: root.anchorItem
    owner: root.hostWidget || root
    bar: root.bar
    open: root.opened
    padding: Style.spacing.panelPadding
    contentWidth: Style.space(360)
    contentHeight: Style.space(440)

    PanelKeyCatcher {
      id: keyCatcher
      anchors.fill: parent
      onCloseRequested: root.close()

      Column {
        id: content
        width: parent.width
        anchors.centerIn: parent
        spacing: Style.space(4)

        // ---- Header: backend state + global speeds.
        Row {
          width: parent.width
          spacing: Style.space(2)

          Rectangle {
            width: Style.space(3)
            height: Style.space(3)
            radius: width / 2
            anchors.verticalCenter: parent.verticalCenter
            color: root.stateColor
          }
          Text {
            anchors.verticalCenter: parent.verticalCenter
            text: root.headerState
            color: root.barForeground
            opacity: 0.62
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }
          Item { width: parent.width - headerSpeeds.implicitWidth - Style.space(8); height: 1 }
          Text {
            id: headerSpeeds
            anchors.verticalCenter: parent.verticalCenter
            text: "\u2193 " + root.formatSpeed(root.dlSpeed) + "   \u2191 " + root.formatSpeed(root.upSpeed)
            color: root.barForeground
            opacity: 0.8
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }
        }

        // ---- Filters.
        Row {
          width: parent.width
          spacing: Style.space(3)

          Repeater {
            model: root.filters

            Text {
              required property string modelData
              readonly property bool active: root.filter === modelData
              text: modelData.charAt(0).toUpperCase() + modelData.slice(1)
              color: active ? Color.accent : root.barForeground
              opacity: active ? 1.0 : 0.55
              font.pixelSize: Style.font.caption
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
              MouseArea {
                anchors.fill: parent
                cursorShape: Qt.PointingHandCursor
                onClicked: root.setFilter(modelData)
              }
            }
          }
        }

        // ---- Torrent list.
        ListView {
          id: listView
          width: parent.width
          height: Math.min(contentHeight, Style.space(340))
          spacing: Style.space(2)
          clip: true
          boundsBehavior: Flickable.StopAtBounds
          interactive: contentHeight > height

          ScrollBar.vertical: ScrollBar { policy: ScrollBar.AsNeeded }

          model: ListModel { id: view }

          delegate: Item {
            width: ListView.view.width
            height: Style.space(13)

            Column {
              width: parent.width
              anchors.verticalCenter: parent.verticalCenter
              spacing: Style.space(1)

              Row {
                width: parent.width
                spacing: Style.space(2)

                Text {
                  id: nameText
                  text: name
                  color: root.barForeground
                  opacity: 0.9
                  font.pixelSize: Style.font.body
                  font.family: root.bar ? root.bar.fontFamily : Style.font.family
                  elide: Text.ElideRight
                  width: parent.width - stateText.implicitWidth - speedsText.implicitWidth - Style.space(4)
                }
                Text {
                  id: stateText
                  text: state
                  color: state === "downloading" ? Color.accent : root.barForeground
                  opacity: 0.55
                  font.pixelSize: Style.font.caption
                  font.family: root.bar ? root.bar.fontFamily : Style.font.family
                }
                Item { width: parent.width - nameText.width - stateText.implicitWidth - speedsText.implicitWidth - Style.space(4); height: 1 }
                Text {
                  id: speedsText
                  text: (dlspeed > 0 ? "\u2193 " + root.formatSpeed(dlspeed) + " " : "") +
                        (upspeed > 0 ? "\u2191 " + root.formatSpeed(upspeed) : "") +
                        (dlspeed === 0 && upspeed === 0 ? root.formatEta(eta) + "  " + ratio.toFixed(2) : "")
                  color: root.barForeground
                  opacity: 0.65
                  font.pixelSize: Style.font.caption
                  font.family: root.bar ? root.bar.fontFamily : Style.font.family
                }
              }

              // Progress track: thin, native radius, accent fill.
              Rectangle {
                width: parent.width
                height: Math.max(2, Style.space(0.75))
                radius: height / 2
                color: Color.muted
                opacity: 0.25

                Rectangle {
                  width: Math.min(1, Math.max(0, progress)) * parent.width
                  height: parent.height
                  radius: parent.radius
                  color: progress >= 1 ? Color.muted : Color.accent
                }
              }
            }
          }

          // Empty / degraded states.
          Text {
            visible: view.count === 0 && root.backendOk
            anchors.centerIn: parent
            text: root.order.length === 0 ? "No torrents" : "Nothing in this filter"
            color: root.barForeground
            opacity: 0.45
            font.pixelSize: Style.font.body
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }
          Text {
            visible: !root.daemonUp || !root.backendOk
            anchors.centerIn: parent
            text: !root.daemonUp ? "omatorrent-service offline" : "qBittorrent unreachable — showing last known state"
            color: root.bar ? root.bar.urgent : Color.urgent
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }
        }
      }
    }
  }

  // ---- Subscription + status client (own connection; lifecycle tied to
  //      this panel, which stays loaded with its host widget).
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
      root.requestStatus()
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
