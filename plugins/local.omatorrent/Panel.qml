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
  // One request in flight at any time, across ALL request types
  // (docs/IPC.md client rules): pendingKind is "" | "status" |
  // "subscribe" and clears only on the matching id-verified response.
  property int pendingId: -1
  property string pendingKind: ""
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

  // ---- Mutations (v1.2, ADR-0006). Presentation-only intents; the
  //      daemon validates, submits and confirms from committed state.
  //      Pending overlays are cosmetic — committed state stays the only
  //      authority and everything pending is dropped on disconnect.
  property var pendingMutations: ({}) // hash -> {action, mutation?}
  property string mutError: ""
  property real mutErrorSince: 0
  property bool addOpen: false
  property string addText: ""
  property string confirmHash: ""
  property string confirmName: ""
  property int mutSeq: 0 // ref uniqueness within this panel instance
  property string pendingMutHash: "" // hash of the in-flight mutation request

  readonly property string xdgRuntime: Quickshell.env("XDG_RUNTIME_DIR") || ""
  readonly property string socketPath: xdgRuntime !== "" ? xdgRuntime + "/omatorrent/service.sock" : ""

  readonly property var filters: ["all", "active", "downloading", "seeding", "paused", "completed"]

  // Presentation-safe add validation only (daemon is authoritative).
  readonly property bool addValid: {
    const u = addText.trim()
    return u.startsWith("magnet:") && u.length <= 2048
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

  // ---- Mutation client discipline (extends the one-in-flight rule to
  //      "mutation"; ref is unique per ATTEMPT, never reused).
  function newRef() {
    mutSeq++
    return "p" + Date.now() + "-" + mutSeq
  }

  function requestMutation(action, extra) {
    if (pendingKind !== "" || !sock.connected || !helloSent) return false
    pendingKind = "mutation"
    pendingId = nextId++
    pendingSince = Date.now()
    pendingMutHash = extra && typeof extra.hash === "string" ? extra.hash : ""
    const msg = Object.assign({ type: action, id: pendingId, ref: newRef() }, extra)
    send(msg)
    return true
  }

  function togglePause(hash) {
    const t = torrents[hash]
    if (!t) return
    const action = t.state === "paused" ? "torrent.resume" : "torrent.pause"
    if (!requestMutation(action, { hash: hash })) return
    pendingMutations[hash] = { action: action }
    pendingMutationsChanged()
  }

  function addMagnet() {
    const url = addText.trim()
    // Presentation-safe basics only; the daemon is the authority.
    if (!url.startsWith("magnet:") || url.length > 2048) return
    if (!requestMutation("torrent.add", { url: url })) return
    addOpen = false
    addText = ""
    mutError = ""
  }

  function requestRemove(hash, deleteFiles) {
    const t = torrents[hash]
    if (!t) return
    // delete_files is an explicit boolean at every layer (ADR-0006).
    if (!requestMutation("torrent.remove", { hash: hash, delete_files: deleteFiles })) return
    pendingMutations[hash] = { action: "torrent.remove" }
    pendingMutationsChanged()
    confirmHash = ""
    confirmName = ""
  }

  function showMutationOutcome(action, hash, status, code) {
    if (status === "confirmed") {
      const p = pendingMutations[hash]
      if (p && p.action === action) {
        delete pendingMutations[hash]
        pendingMutationsChanged()
      }
      mutError = ""
      return
    }
    if (status === "timeout") {
      // Ambiguous, not failed: committed state remains the authority.
      const p2 = pendingMutations[hash]
      if (p2 && p2.action === action) {
        delete pendingMutations[hash]
        pendingMutationsChanged()
      }
      mutError = "Action result unknown — state will tell"
      mutErrorSince = Date.now()
      return
    }
    // Stage-1 rejection codes (deterministic model, docs/IPC.md v1.2).
    const messages = {
      stale_torrent: "Torrent no longer exists",
      invalid_url: "Invalid magnet link",
      duplicate: "Torrent already added",
      backend_rejected: "qBittorrent refused the action",
      backend_unavailable: "qBittorrent unreachable",
      busy: "Too many actions in flight",
      ref_conflict: "Action conflict — try again"
    }
    mutError = messages[code] || "Action failed"
    mutErrorSince = Date.now()
    if (code === "stale_torrent") {
      delete pendingMutations[hash]
      pendingMutationsChanged()
    }
  }

  function resetSession() {
    helloSent = false
    subscribed = false
    pendingId = -1
    pendingKind = ""
    daemonUp = false
    backendOk = false
    dlSpeed = 0
    upSpeed = 0
    // Never carry optimistic state across a connection loss: the fresh
    // snapshot after resubscribe is the truth (ADR-0006 client rules).
    pendingMutations = ({})
    pendingMutationsChanged()
    confirmHash = ""
    confirmName = ""
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
    if (applyingSnapshot) {
      // Snapshot items only populate the map: ordering and view are
      // built once at snapshot.end (O(N log N), not per-item splice).
      torrents[item.hash] = item
      return
    }
    const prev = torrents[item.hash]
    const isNew = prev === undefined
    const renamed = !isNew && prev.name !== item.name
    torrents[item.hash] = item
    if (isNew) {
      insertSorted(item.hash, item)
    } else if (renamed) {
      // Keep the sorted order truthful across renames: reposition the
      // hash, then rebuild the view once (order changed).
      const i = order.indexOf(item.hash)
      if (i >= 0) order.splice(i, 1)
      insertSorted(item.hash, item)
    }
    if (isNew || renamed) {
      rebuildView()
      return
    }
    syncRow(item.hash, item)
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
      dlspeed: (typeof t.dlspeed === "number" && isFinite(t.dlspeed)) ? t.dlspeed : 0,
      upspeed: (typeof t.upspeed === "number" && isFinite(t.upspeed)) ? t.upspeed : 0,
      eta: (typeof t.eta === "number" && isFinite(t.eta)) ? t.eta : 8640000,
      ratio: (typeof t.ratio === "number" && isFinite(t.ratio)) ? t.ratio : 0
    }
  }

  function syncRow(hash, item) {
    if (matchesFilter(item)) {
      const i = viewIndex(hash)
      if (i >= 0) {
        view.set(i, row(item))
        return
      }
    }
    rebuildView() // filter membership changed
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
        if (pendingKind !== "status" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
        pendingKind = ""
        daemonUp = true
        backendOk = msg.qbittorrent === "ok"
        dlSpeed = msg.dl_speed || 0
        upSpeed = msg.up_speed || 0
        // Exactly one request in flight: subscribe only after the status
        // response, and only once per session.
        if (!subscribed) requestSubscribe()
        break
      case "torrent.subscribed":
        if (pendingKind !== "subscribe" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
        pendingKind = ""
        subscribed = true
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
      case "error":
        break
      case "torrent.snapshot.end":
        // Build the ordering once from the collected map and sort it:
        // O(N log N) snapshot construction, one view rebuild.
        order = Object.keys(torrents)
        order.sort(function (a, b) {
          if (torrents[a].name !== torrents[b].name) return torrents[a].name < torrents[b].name ? -1 : 1
          return a < b ? -1 : (a > b ? 1 : 0)
        })
        applyingSnapshot = false
        rebuildView()
        break
      case "mutation.accepted":
        // Stage 1 (ADR-0006): submitted, NOT success. The overlay stays
        // until the state-derived result arrives.
        if (pendingKind !== "mutation" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
        pendingKind = ""
        if (msg.action === "torrent.add" && typeof msg.hash === "string") {
          pendingMutations[msg.hash] = { action: "torrent.add", mutation: msg.mutation }
          pendingMutationsChanged()
        } else if (pendingMutHash !== "" && pendingMutations[pendingMutHash] !== undefined &&
                   pendingMutations[pendingMutHash].action === msg.action) {
          pendingMutations[pendingMutHash].mutation = msg.mutation
        }
        pendingMutHash = ""
        break
      case "mutation.rejected":
        if (pendingKind !== "mutation" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
        pendingKind = ""
        showMutationOutcome("torrent.reject", pendingMutHash, "", msg.code)
        pendingMutHash = ""
        break
      case "mutation.result":
        if (typeof msg.id === "number") {
          // Replay answer to a retried request: also the stage-1 response.
          if (pendingKind !== "mutation" || msg.id !== pendingId) return
          pendingId = -1
          pendingKind = ""
          pendingMutHash = ""
        }
        if (typeof msg.action === "string" && typeof msg.hash === "string") {
          showMutationOutcome(msg.action, msg.hash, msg.status, "")
        }
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
      + (root.addOpen ? Style.space(12) : 0)
      + (root.confirmHash !== "" ? Style.space(13) : 0)
      + (root.mutError !== "" ? Style.space(6) : 0)

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
          Item { width: parent.width - headerSpeeds.implicitWidth - headerAdd.implicitWidth - Style.space(10); height: 1 }
          Text {
            id: headerSpeeds
            anchors.verticalCenter: parent.verticalCenter
            text: "\u2193 " + root.formatSpeed(root.dlSpeed) + "   \u2191 " + root.formatSpeed(root.upSpeed)
            color: root.barForeground
            opacity: 0.8
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }
          // Add magnet entry point (daemon validates authoritatively).
          Text {
            id: headerAdd
            anchors.verticalCenter: parent.verticalCenter
            text: root.addOpen ? "\u00D7" : "+"
            color: root.addOpen ? Color.accent : root.barForeground
            opacity: 0.8
            font.pixelSize: Style.font.body
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            MouseArea {
              anchors.fill: parent
              cursorShape: Qt.PointingHandCursor
              onClicked: {
                root.addOpen = !root.addOpen
                if (!root.addOpen) root.addText = ""
              }
            }
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

        // ---- Add magnet (simple native input; presentation-safe
        //      validation only, the daemon is the authority).
        Row {
          visible: root.addOpen
          width: parent.width
          spacing: Style.space(3)

          TextField {
            id: addInput
            width: parent.width - addConfirm.implicitWidth - Style.space(6)
            anchors.verticalCenter: parent.verticalCenter
            text: root.addText
            placeholderText: "magnet:?xt=urn:btih:\u2026"
            color: root.barForeground
            opacity: 0.9
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            background: Rectangle {
              radius: Style.space(2)
              color: Color.muted
              opacity: 0.18
            }
            onTextEdited: root.addText = addInput.text
            onAccepted: root.addMagnet()
          }
          Text {
            id: addConfirm
            anchors.verticalCenter: parent.verticalCenter
            text: "Add"
            color: Color.accent
            opacity: root.addValid ? 1.0 : 0.35
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            MouseArea {
              anchors.fill: parent
              cursorShape: Qt.PointingHandCursor
              onClicked: root.addMagnet()
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

            HoverHandler { id: rowHover }
            readonly property bool mutPending: root.pendingMutations[hash] !== undefined
            readonly property real rightWidth: Math.max(speedsText.visible ? speedsText.implicitWidth : 0,
                                                        actionsRow.implicitWidth)

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
                  opacity: mutPending ? 0.55 : 0.9
                  font.pixelSize: Style.font.body
                  font.family: root.bar ? root.bar.fontFamily : Style.font.family
                  elide: Text.ElideRight
                  width: parent.width - stateText.implicitWidth - rightWidth - Style.space(4)
                }
                Text {
                  id: stateText
                  text: {
                    // Pending overlay: cosmetic only, cleared by the
                    // daemon's state-derived result (never optimistic).
                    const p = root.pendingMutations[hash]
                    if (p) {
                      if (p.action === "torrent.pause") return "pausing\u2026"
                      if (p.action === "torrent.resume") return "resuming\u2026"
                      if (p.action === "torrent.remove") return "removing\u2026"
                      if (p.action === "torrent.add") return "adding\u2026"
                    }
                    return state
                  }
                  color: mutPending || state === "downloading" ? Color.accent : root.barForeground
                  opacity: mutPending ? 0.9 : 0.55
                  font.pixelSize: Style.font.caption
                  font.family: root.bar ? root.bar.fontFamily : Style.font.family
                }
                Item { width: parent.width - nameText.width - stateText.implicitWidth - rightWidth - Style.space(4); height: 1 }
                Text {
                  id: speedsText
                  visible: !actionsRow.visible
                  text: (dlspeed > 0 ? "\u2193 " + root.formatSpeed(dlspeed) + " " : "") +
                        (upspeed > 0 ? "\u2191 " + root.formatSpeed(upspeed) : "") +
                        (dlspeed === 0 && upspeed === 0 ? root.formatEta(eta) + "  " + ratio.toFixed(2) : "")
                  color: root.barForeground
                  opacity: 0.65
                  font.pixelSize: Style.font.caption
                  font.family: root.bar ? root.bar.fontFamily : Style.font.family
                }
                Row {
                  id: actionsRow
                  visible: rowHover.hovered && root.daemonUp && root.subscribed
                  spacing: Style.space(3)
                  Text {
                    text: state === "paused" ? "\u25B6" : "\u2016"
                    color: Color.accent
                    font.pixelSize: Style.font.caption
                    font.family: root.bar ? root.bar.fontFamily : Style.font.family
                    MouseArea {
                      anchors.fill: parent
                      cursorShape: Qt.PointingHandCursor
                      onClicked: root.togglePause(hash)
                    }
                  }
                  Text {
                    text: "\u2715"
                    color: root.bar ? root.bar.urgent : Color.urgent
                    font.pixelSize: Style.font.caption
                    font.family: root.bar ? root.bar.fontFamily : Style.font.family
                    MouseArea {
                      anchors.fill: parent
                      cursorShape: Qt.PointingHandCursor
                      onClicked: {
                        root.confirmHash = hash
                        root.confirmName = name
                      }
                    }
                  }
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

        // ---- Removal confirmation: two EXPLICIT paths; the delete-files
        //      one is visually urgent and separately confirmed. The daemon
        //      requires the boolean — nothing is inferred (ADR-0006).
        Column {
          visible: root.confirmHash !== ""
          width: parent.width
          spacing: Style.space(2)

          Text {
            width: parent.width
            elide: Text.ElideRight
            text: "Remove \u201C" + root.confirmName + "\u201D?"
            color: root.barForeground
            opacity: 0.9
            font.pixelSize: Style.font.body
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }
          Row {
            spacing: Style.space(4)

            Text {
              text: "Remove"
              color: Color.accent
              opacity: 0.9
              font.pixelSize: Style.font.caption
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
              MouseArea {
                anchors.fill: parent
                cursorShape: Qt.PointingHandCursor
                onClicked: root.requestRemove(root.confirmHash, false)
              }
            }
            Text {
              text: "Remove + delete files"
              color: root.bar ? root.bar.urgent : Color.urgent
              opacity: 0.95
              font.pixelSize: Style.font.caption
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
              MouseArea {
                anchors.fill: parent
                cursorShape: Qt.PointingHandCursor
                onClicked: root.requestRemove(root.confirmHash, true)
              }
            }
            Text {
              text: "Cancel"
              color: root.barForeground
              opacity: 0.55
              font.pixelSize: Style.font.caption
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
              MouseArea {
                anchors.fill: parent
                cursorShape: Qt.PointingHandCursor
                onClicked: {
                  root.confirmHash = ""
                  root.confirmName = ""
                }
              }
            }
          }
        }

        // ---- Mutation outcome banner (truthful success/failure/ambiguous).
        Text {
          visible: root.mutError !== ""
          width: parent.width
          elide: Text.ElideRight
          text: root.mutError
          color: root.bar ? root.bar.urgent : Color.urgent
          opacity: 0.9
          font.pixelSize: Style.font.caption
          font.family: root.bar ? root.bar.fontFamily : Style.font.family
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
      if (root.pendingKind !== "") {
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

  // Mutation outcome banner expiry (truthful, transient).
  Timer {
    interval: 1000
    running: root.mutError !== ""
    repeat: true
    onTriggered: if (Date.now() - root.mutErrorSince > 6000) root.mutError = ""
  }

  Component.onCompleted: if (root.socketPath !== "") sock.connected = true
}
