import QtQuick
import QtQuick.Controls
import Quickshell
import Quickshell.Io
import qs.Commons
import qs.Ui
import "MutationClient.js" as MC

// OmaTorrent torrent panel: presentation only (ADR-0001). Renders the
// daemon's normalized torrent state over IPC v1.1 (ADR-0005): one
// subscription delivers a full snapshot then bounded delta frames; the
// header polls v1.0 system.status for backend state and global speeds.
// Mutations (v1.2, ADR-0006) go through MutationClient.js — an
// order-independent correlation state machine (request responses and
// terminal pushes may arrive in either legal order). No qBittorrent API
// knowledge here — hashes arrive from the daemon and merge semantics
// are the daemon's.
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
  //      Correlation lives in MutationClient.js (shared with the
  //      deterministic ordering tests): pending overlays are cosmetic,
  //      cleared exclusively by terminal outcomes or disconnect, and
  //      request responses / terminal pushes are order-independent.
  property var mutState: MC.newState()
  property string mutError: ""
  property real mutErrorSince: 0
  property bool addOpen: false
  property string addText: ""
  property string confirmHash: ""
  property string confirmName: ""
  property int mutSeq: 0 // ref uniqueness within this panel instance

  // ---- Connection (v1.4, ADR-0008): status poll + settings mode.
  //      connInfo mirrors the daemon's connection.status payload; the
  //      stored secret NEVER crosses back (has_secret only).
  property var connInfo: null
  property bool settingsOpen: false
  property string addrText: ""
  property string userText: ""
  property string passText: ""       // transient; cleared after the frame is written
  property bool forgetSecret: false  // explicit delete intent (replaces keep)
  property string pinCandidate: ""   // fingerprint offered by a failed untrusted test
  property bool pinTrusted: false    // a pinned test has succeeded
  property bool allowInsecure: false // user acknowledged non-loopback HTTP
  property var testResult: null      // last connection.test payload
  property bool connBusy: false      // test/configure in flight
  property string connError: ""
  property real connErrorSince: 0
  // Watchdog: a pending mutation whose terminal never arrives (e.g. a
  // result lost while the daemon's delivery pump resubscribed) is
  // resolved by re-sending the SAME ref (daemon replays the recorded
  // outcome — no backend execution). torrent.remove is NEVER re-sent:
  // it escalates to the ambiguity banner (docs/IPC.md client rules).
  readonly property real mutWatchdogMs: 12000

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
  //      "mutation"; ref is unique per ATTEMPT, never reused across
  //      attempts — the watchdog re-sends the SAME ref only).
  function newRef() {
    mutSeq++
    return "p" + Date.now() + "-" + mutSeq
  }

  // requestMutation is used BOTH for fresh attempts (fresh ref) and for
  // same-ref watchdog queries (recorded outcome replay, no execution).
  function requestMutation(action, extra, refOverride) {
    if (pendingKind !== "" || !sock.connected || !helloSent) return false
    pendingKind = "mutation"
    pendingId = nextId++
    pendingSince = Date.now()
    const ref = refOverride !== undefined ? refOverride : newRef()
    const hash = extra && typeof extra.hash === "string" ? extra.hash : ""
    const url = extra && typeof extra.url === "string" ? extra.url : ""
    const del = extra && extra.delete_files === true
    MC.begin(mutState, pendingId, action, hash, url, del, ref, Date.now())
    send(Object.assign({ type: action, id: pendingId, ref: ref }, extra))
    mutStateChanged()
    return true
  }

  function togglePause(hash) {
    const t = torrents[hash]
    if (!t) return
    requestMutation(t.state === "paused" ? "torrent.resume" : "torrent.pause", { hash: hash })
  }

  function addMagnet() {
    const url = addText.trim()
    // Presentation-safe basics only; the daemon is the authority. The
    // guard matters: a schema-invalid frame would close the connection.
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
    confirmHash = ""
    confirmName = ""
  }

  // ---- Terminal effect mapping (banner truthfulness only; overlays
  //      are settled inside MutationClient).
  function bannerForCode(code) {
    const messages = {
      stale_torrent: "Torrent no longer exists",
      invalid_url: "Invalid magnet link",
      duplicate: "Torrent already added",
      backend_rejected: "qBittorrent refused the action",
      backend_unavailable: "qBittorrent unreachable",
      busy: "Too many actions in flight",
      ref_conflict: "Action conflict — try again"
    }
    return messages[code] || "Action failed"
  }

  function testStatusText() {
    if (!testResult) return ""
    const labels = {
      auth_required: "Authentication required",
      auth_failed: "Authentication failed — host reached, credentials rejected",
      banned: "Temporarily banned by the host",
      tls_untrusted: "Certificate verification failed",
      tls_hostname: "Certificate not valid for this host",
      secrets_unavailable: "Credential store unavailable",
      insecure_http: "Plain HTTP to a remote host",
      invalid_configuration: "Invalid address",
      unreachable: "Host unreachable",
      backend_error: "Unexpected response from host"
    }
    return labels[testResult.status] || "Connection failed"
  }

  function connRejectionBanner(code) {
    const messages = {
      invalid_url: "Invalid address or security settings",
      insecure_http: "Plain HTTP needs explicit acknowledgement",
      pin_unknown: "Certificate must be re-tested first",
      secrets_unavailable: "Credential store unavailable or locked",
      mutations_pending: "Wait for pending actions, then save again",
      storage_error: "Saving the configuration failed"
    }
    return messages[code] || "Configuration rejected"
  }

  function applyTerminalEffect(t) {
    // Ambiguous, not failed: committed state remains the authority.
    mutError = t.status === "timeout" ? "Action result unknown — state will tell" : ""
    if (mutError !== "") mutErrorSince = Date.now()
  }

  // Watchdog pass: resolve pending mutations whose terminal never
  // arrived. Same-ref replay query for pause/resume/add (the daemon
  // answers from its recorded outcome — no backend execution);
  // torrent.remove is NEVER re-sent: its overlay escalates to the
  // ambiguity banner and the state stream itself settles the row.
  function mutationWatchdog() {
    if (!sock.connected || !helloSent || pendingKind !== "") return
    const now = Date.now()
    for (const hash of Object.keys(mutState.pending)) {
      const e = mutState.pending[hash]
      if (!e || now - e.since < mutWatchdogMs || e.queried) continue
      if (e.action === "torrent.remove") {
        delete mutState.pending[hash]
        mutError = "Action result unknown — state will tell"
        mutErrorSince = now
        mutStateChanged()
        continue
      }
      const extra = e.action === "torrent.add" ? { url: e.url } : { hash: e.hash }
      if (requestMutation(e.action, extra, e.ref)) {
        // pending entries are keyed by e.hash in every case; keep the
        // queried latch so a lost replay answer cannot loop the query.
        if (mutState.pending[e.hash] !== undefined) mutState.pending[e.hash].queried = true
        mutStateChanged()
      }
      return // one watchdog query at a time (lockstep)
    }
  }

  // ---- Connection client discipline: one in flight (pendingKind
  //      extends to "connection" | "test" | "configure").
  function requestConnectionStatus() {
    if (pendingKind !== "") return
    pendingKind = "connection"
    pendingId = nextId++
    pendingSince = Date.now()
    send({ type: "connection.status", id: pendingId })
  }

  function connTlsMode() {
    return pinTrusted && pinCandidate !== "" ? "pin" : "system"
  }

  function runTest() {
    if (pendingKind !== "" || !sock.connected || !helloSent) return
    const addr = addrText.trim()
    if (addr === "") return
    connBusy = true
    connError = ""
    pendingKind = "test"
    pendingId = nextId++
    pendingSince = Date.now()
    const msg = { type: "connection.test", id: pendingId, url: addr,
                  username: userText.trim(), tls_mode: connTlsMode() }
    if (passText !== "") msg.password = passText
    else if (userText.trim() !== "" && connInfo && connInfo.has_secret === true) msg.use_stored_password = true
    if (connTlsMode() === "pin") msg.pin = pinCandidate
    if (allowInsecure) msg.allow_insecure_http = true
    send(msg)
    if (passText !== "") passText = "" // transient; never kept in QML longer than the send
  }

  function saveConnection() {
    if (pendingKind !== "" || !sock.connected || !helloSent) return
    const addr = addrText.trim()
    if (addr === "") return
    const action = passText !== "" ? "replace" : (forgetSecret ? "delete" : "keep")
    connBusy = true
    connError = ""
    pendingKind = "configure"
    pendingId = nextId++
    pendingSince = Date.now()
    const msg = { type: "connection.configure", id: pendingId, url: addr,
                  username: userText.trim(), secret_action: action,
                  tls_mode: connTlsMode() }
    if (action === "replace") msg.password = passText
    if (connTlsMode() === "pin") msg.pin = pinCandidate
    if (allowInsecure) msg.allow_insecure_http = true
    send(msg)
    if (action === "replace") passText = ""
  }

  function openSettings() {
    if (!settingsOpen) {
      settingsOpen = true
      // Prefill from daemon truth (non-secret metadata only).
      if (connInfo) {
        addrText = typeof connInfo.url === "string" ? connInfo.url : addrText
        userText = typeof connInfo.username === "string" ? connInfo.username : userText
        // An active pin persists across saves: keep its fingerprint so
        // Save re-sends pin mode instead of silently downgrading trust.
        if (connInfo.tls_mode === "pin" && typeof connInfo.pin === "string" && connInfo.pin !== "") {
          pinCandidate = connInfo.pin
          pinTrusted = true
        }
      }
      testResult = null
      connError = ""
      requestConnectionStatus()
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
    // Early-result buffers die with the connection too — a mutation id
    // from a previous session must never resolve a new session's state.
    MC.reset(mutState)
    mutStateChanged()
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
        // response, and only once per session; connection status is
        // chained after both (settings/degraded banners need it).
        if (!subscribed) requestSubscribe()
        else requestConnectionStatus()
        break
      case "connection.status":
        if (pendingKind !== "connection" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
        pendingKind = ""
        connInfo = msg
        break
      case "connection.test":
        if (pendingKind !== "test" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
        pendingKind = ""
        connBusy = false
        testResult = msg
        // An untrusted certificate offers its fingerprint: the explicit
        // trust flow (pin) starts from THIS daemon-captured value; the
        // certificate bytes never cross the wire (ADR-0008).
        if (msg.result !== "ok" && msg.status === "tls_untrusted" &&
            typeof msg.offered_fingerprint === "string" && msg.offered_fingerprint !== "") {
          if (pinCandidate !== msg.offered_fingerprint) {
            pinCandidate = msg.offered_fingerprint
            pinTrusted = false
          }
        }
        break
      case "connection.configured":
        if (pendingKind !== "configure" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
        pendingKind = ""
        connBusy = false
        connError = ""
        forgetSecret = false
        testResult = null
        // The daemon switches the backend epoch: the subscription
        // delivers the old torrents as removals plus a fresh snapshot
        // of the new backend — nothing stale survives (ADR-0008 §8).
        requestConnectionStatus()
        break
      case "connection.rejected":
        if (pendingKind !== "configure" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
        pendingKind = ""
        connBusy = false
        connError = connRejectionBanner(typeof msg.code === "string" ? msg.code : "")
        connErrorSince = Date.now()
        // A pin that lost its captured certificate: re-test recaptures.
        if (msg.code === "pin_unknown") pinTrusted = false
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
        // Stage 1 (ADR-0006): submitted, NOT success. Responses and
        // terminal pushes are order-independent (MutationClient): if the
        // terminal already arrived, it is applied HERE and no pending
        // overlay is created.
        if (pendingKind !== "mutation" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
        pendingKind = ""
        {
          const r = MC.onAccepted(mutState, msg)
          if (r.handled && r.applied) applyTerminalEffect(r.applied)
          mutStateChanged()
        }
        break
      case "mutation.rejected":
        if (pendingKind !== "mutation" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingId = -1
        pendingKind = ""
        {
          const r = MC.onRejected(mutState, msg)
          if (r.handled) {
            mutError = bannerForCode(r.code)
            mutErrorSince = Date.now()
          }
          mutStateChanged()
        }
        break
      case "mutation.result":
        if (typeof msg.id === "number") {
          // Replay answer (same-ref query or replayed request): the
          // terminal for the CURRENT request.
          if (pendingKind !== "mutation" || msg.id !== pendingId) return
          pendingId = -1
          pendingKind = ""
          const r = MC.onResultResponse(mutState, msg)
          if (r.handled) applyTerminalEffect(r)
          mutStateChanged()
          break
        }
        // Pushed terminal: applied when its mutation is known, buffered
        // as an early result otherwise (harmless for foreign/duplicate).
        if (typeof msg.action === "string" && typeof msg.hash === "string" &&
            typeof msg.mutation === "number") {
          const r = MC.onResultPush(mutState, msg)
          if (r.applied) applyTerminalEffect(r)
          mutStateChanged()
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

  // Connection-class header text (v1.4): differentiated degraded
  // states, display-safe host label for remote backends. Never a
  // username, password or full URL.
  readonly property string connHostLabel: {
    if (!connInfo) return ""
    if (connInfo.mode !== "remote") return ""
    return typeof connInfo.host === "string" ? connInfo.host : ""
  }
  readonly property string headerState: {
    if (!daemonUp) return "daemon offline"
    if (connInfo) {
      switch (connInfo.status) {
        case "connected": return connHostLabel !== "" ? "connected · " + connHostLabel : "connected"
        case "auth_required": return "authentication required"
        case "auth_failed": return "authentication failed"
        case "banned": return "temporarily banned"
        case "tls_untrusted": return "secure connection failed"
        case "tls_hostname": return "certificate hostname mismatch"
        case "secrets_unavailable": return "credential store locked"
        case "invalid_configuration": return "invalid connection settings"
      }
      if (connInfo.insecure === true && connInfo.status === "connected") {
        return "connected (insecure) · " + (connHostLabel !== "" ? connHostLabel : "remote")
      }
    }
    return backendOk ? "connected" : "qBittorrent unreachable"
  }

  // Degraded connection callouts (truthful, one line + settings path).
  readonly property bool connNeedsAttention: {
    if (!daemonUp || !connInfo) return false
    switch (connInfo.status) {
      case "auth_required":
      case "auth_failed":
      case "banned":
      case "tls_untrusted":
      case "tls_hostname":
      case "secrets_unavailable":
      case "invalid_configuration":
        return true
    }
    return false
  }
  readonly property string connAttentionText: {
    if (!connInfo) return ""
    switch (connInfo.status) {
      case "auth_required": return "qBittorrent requires authentication and none is configured."
      case "auth_failed": return "qBittorrent rejected the configured credentials."
      case "banned": return "Too many failed logins — the backend banned this machine temporarily."
      case "tls_untrusted": return "Certificate verification failed. The connection was NOT downgraded."
      case "tls_hostname": return "The certificate is not valid for this host. The connection was NOT downgraded."
      case "secrets_unavailable": return "The credential store is unavailable or locked."
      case "invalid_configuration": return "The stored connection settings are invalid."
    }
    return ""
  }

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
    contentHeight: root.settingsOpen ? Style.space(430)
      : Style.space(440)
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
          Item { width: parent.width - headerSpeeds.implicitWidth - headerDash.implicitWidth - headerAdd.implicitWidth - Style.space(12); height: 1 }
          Text {
            id: headerSpeeds
            anchors.verticalCenter: parent.verticalCenter
            text: "\u2193 " + root.formatSpeed(root.dlSpeed) + "   \u2191 " + root.formatSpeed(root.upSpeed)
            color: root.barForeground
            opacity: 0.8
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }
          // Connection settings entry (Phase 0.5): one canonical
          // settings surface as a mode of this panel (native pattern;
          // ADR-0008 §10).
          Text {
            anchors.verticalCenter: parent.verticalCenter
            text: root.settingsOpen ? "\u2190" : "\u2699"
            color: root.settingsOpen ? Color.accent : root.barForeground
            opacity: 0.8
            font.pixelSize: Style.font.body
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            MouseArea {
              anchors.fill: parent
              cursorShape: Qt.PointingHandCursor
              onClicked: {
                if (root.settingsOpen) {
                  root.settingsOpen = false
                  root.passText = "" // abandoned forms must not retain secrets
                } else {
                  root.openSettings()
                }
              }
            }
          }
          // Dashboard entry (Phase 0.4): opens the companion overlay
          // plugin through the first-party shell routing (the omarchy
          // menu bar-widget pattern) — this popout closes with it.
          Text {
            id: headerDash
            anchors.verticalCenter: parent.verticalCenter
            text: "\u25A4"
            color: Color.accent
            opacity: 0.8
            font.pixelSize: Style.font.body
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            MouseArea {
              anchors.fill: parent
              cursorShape: Qt.PointingHandCursor
              onClicked: {
                if (root.bar && typeof root.bar.run === "function") {
                  root.close()
                  root.bar.run("omarchy-shell shell toggle local.omatorrent-dashboard")
                }
              }
            }
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

        // ---- Connection attention banner (auth/TLS/store failures:
        //      truthful, differentiated, one path to settings — ASCII
        //      projections B/C of ADR-0008).
        Column {
          visible: root.connNeedsAttention && !root.settingsOpen
          width: parent.width
          spacing: Style.space(2)

          Text {
            width: parent.width
            wrapMode: Text.WordWrap
            text: root.connAttentionText
            color: root.bar ? root.bar.urgent : Color.urgent
            opacity: 0.9
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }
          Text {
            text: "Connection settings"
            color: Color.accent
            opacity: 0.9
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            MouseArea {
              anchors.fill: parent
              cursorShape: Qt.PointingHandCursor
              onClicked: root.openSettings()
            }
          }
        }

        // ---- Insecure remote HTTP badge (acknowledged state is
        //      prominently marked; the acknowledgement flow itself
        //      lives in settings — one canonical surface).
        Text {
          visible: root.connInfo && root.connInfo.insecure === true && root.connInfo.status === "connected" && !root.settingsOpen
          width: parent.width
          elide: Text.ElideRight
          text: "insecure connection — traffic is not encrypted"
          color: root.bar ? root.bar.urgent : Color.urgent
          opacity: 0.85
          font.pixelSize: Style.font.caption
          font.family: root.bar ? root.bar.fontFamily : Style.font.family
        }

        // ---- Filters.
        Row {
          visible: !root.settingsOpen
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
          visible: root.addOpen && !root.settingsOpen
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
          visible: !root.settingsOpen
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
            readonly property bool mutPending: root.mutState.pending[hash] !== undefined
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
                    const p = root.mutState.pending[hash]
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
          visible: root.confirmHash !== "" && !root.settingsOpen
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

        // ---- Connection settings (Phase 0.5, ADR-0008 §10): one
        //      canonical form as a mode of this panel. The password is
        //      transient (echoMode masked, cleared after the frame is
        //      written); the stored secret NEVER crosses back into
        //      QML — "stored" renders from has_secret.
        Column {
          visible: root.settingsOpen
          width: parent.width
          spacing: Style.space(3)

          Text {
            text: "qBittorrent connection"
            color: root.barForeground
            opacity: 0.9
            font.pixelSize: Style.font.body
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }

          // Address
          Text {
            text: "Address"
            color: root.barForeground
            opacity: 0.55
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }
          TextField {
            id: addrInput
            width: parent.width
            text: root.addrText
            placeholderText: "https://qbittorrent.home.arpa"
            color: root.barForeground
            opacity: 0.9
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            background: Rectangle {
              radius: Style.space(2)
              color: Color.muted
              opacity: 0.18
            }
            onTextEdited: {
              root.addrText = addrInput.text
              root.testResult = null
              root.connError = ""
              if (root.allowInsecure) root.allowInsecure = false
            }
            onAccepted: root.runTest()
          }

          // Username
          Text {
            text: "Username"
            color: root.barForeground
            opacity: 0.55
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }
          TextField {
            id: userInput
            width: parent.width
            text: root.userText
            placeholderText: "admin"
            color: root.barForeground
            opacity: 0.9
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            background: Rectangle {
              radius: Style.space(2)
              color: Color.muted
              opacity: 0.18
            }
            onTextEdited: {
              root.userText = userInput.text
              root.testResult = null
            }
            onAccepted: root.runTest()
          }

          // Password (write-only: empty = keep/delete per intent)
          Text {
            text: "Password"
            color: root.barForeground
            opacity: 0.55
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }
          TextField {
            id: passInput
            width: parent.width
            text: root.passText
            placeholderText: root.connInfo && root.connInfo.has_secret === true ? "unchanged" : "none (localhost bypass)"
            echoMode: TextInput.Password
            color: root.barForeground
            opacity: 0.9
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
            background: Rectangle {
              radius: Style.space(2)
              color: Color.muted
              opacity: 0.18
            }
            onTextEdited: {
              root.passText = passInput.text
              if (root.passText !== "") root.forgetSecret = false
            }
            onAccepted: root.saveConnection()
          }
          Row {
            width: parent.width
            spacing: Style.space(4)
            Text {
              visible: root.connInfo && root.connInfo.has_secret === true && root.passText === ""
              text: root.forgetSecret ? "stored password will be removed" : "stored securely"
              color: root.forgetSecret ? (root.bar ? root.bar.urgent : Color.urgent) : root.barForeground
              opacity: 0.55
              font.pixelSize: Style.font.caption
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
            }
            Text {
              visible: root.connInfo && root.connInfo.has_secret === true && root.passText === ""
              text: root.forgetSecret ? "Keep" : "Forget"
              color: Color.accent
              opacity: 0.9
              font.pixelSize: Style.font.caption
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
              MouseArea {
                anchors.fill: parent
                cursorShape: Qt.PointingHandCursor
                onClicked: root.forgetSecret = !root.forgetSecret
              }
            }
          }

          // Security: verification is always on (no disable switch at
          // any layer — ADR-0008 §4); the pin flow appears only when a
          // test captured an untrusted certificate.
          Text {
            width: parent.width
            text: "\u2713 Verify TLS certificate"
            color: Color.accent
            opacity: 0.9
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }
          Column {
            visible: root.pinCandidate !== ""
            width: parent.width
            spacing: Style.space(1)
            Text {
              width: parent.width
              elide: Text.ElideMiddle
              text: "offered certificate " + root.pinCandidate
              color: root.barForeground
              opacity: 0.55
              font.pixelSize: Style.font.caption
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
            }
            Text {
              text: root.pinTrusted ? "trusted (pinned)" : "Trust this certificate"
              color: root.pinTrusted ? Color.accent : (root.bar ? root.bar.urgent : Color.urgent)
              opacity: 0.9
              font.pixelSize: Style.font.caption
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
              MouseArea {
                anchors.fill: parent
                cursorShape: Qt.PointingHandCursor
                onClicked: {
                  // Trust = re-test with the pin; the daemon builds the
                  // trust anchor from its OWN captured certificate.
                  if (!root.pinTrusted) {
                    root.pinTrusted = true
                    root.runTest()
                  }
                }
              }
            }
          }

          // Insecure-HTTP acknowledgement (ASCII D): only after a test
          // refused plain HTTP to a non-loopback host.
          Column {
            visible: root.testResult && root.testResult.status === "insecure_http"
            width: parent.width
            spacing: Style.space(2)
            Text {
              width: parent.width
              wrapMode: Text.WordWrap
              text: "Credentials and session data would cross the network without transport encryption."
              color: root.bar ? root.bar.urgent : Color.urgent
              opacity: 0.9
              font.pixelSize: Style.font.caption
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
            }
            Row {
              spacing: Style.space(4)
              Text {
                text: "Use HTTPS instead"
                color: Color.accent
                opacity: 0.9
                font.pixelSize: Style.font.caption
                font.family: root.bar ? root.bar.fontFamily : Style.font.family
                MouseArea {
                  anchors.fill: parent
                  cursorShape: Qt.PointingHandCursor
                  onClicked: {
                    // Nudge the address to https and re-test.
                    const a = root.addrText.trim()
                    if (a.indexOf("http://") === 0) root.addrText = "https://" + a.slice(7)
                    root.testResult = null
                    root.runTest()
                  }
                }
              }
              Text {
                text: "Continue knowingly"
                color: root.bar ? root.bar.urgent : Color.urgent
                opacity: 0.95
                font.pixelSize: Style.font.caption
                font.family: root.bar ? root.bar.fontFamily : Style.font.family
                MouseArea {
                  anchors.fill: parent
                  cursorShape: Qt.PointingHandCursor
                  onClicked: {
                    root.allowInsecure = true
                    root.runTest()
                  }
                }
              }
            }
          }

          // Test result (ASCII F/G): differentiated, never collapsed
          // into "connection failed".
          Column {
            visible: root.testResult !== null
            width: parent.width
            spacing: Style.space(1)

            Text {
              text: root.testResult && root.testResult.result === "ok" ? "\u2713 Connection successful" : "\u2715 " + root.testStatusText()
              color: root.testResult && root.testResult.result === "ok" ? Color.accent : (root.bar ? root.bar.urgent : Color.urgent)
              opacity: 0.95
              font.pixelSize: Style.font.body
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
            }
            Text {
              visible: root.testResult && root.testResult.result === "ok"
              width: parent.width
              text: root.testResult ? ("qBittorrent " + (root.testResult.app_version || "?") +
                      "   WebAPI " + (root.testResult.webapi_version || "?") +
                      "   " + (root.testResult.transport || "?").toUpperCase() +
                      (root.userText.trim() !== "" ? "   authenticated" : "   no authentication")) : ""
              color: root.barForeground
              opacity: 0.7
              font.pixelSize: Style.font.caption
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
            }
            Text {
              visible: root.testResult && root.testResult.result !== "ok" && root.testResult.detail
              width: parent.width
              wrapMode: Text.WordWrap
              text: root.testResult ? (root.testResult.detail || "") : ""
              color: root.barForeground
              opacity: 0.65
              font.pixelSize: Style.font.caption
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
            }
          }

          // Configure rejection banner (fixed strings).
          Text {
            visible: root.connError !== ""
            width: parent.width
            elide: Text.ElideRight
            text: root.connError
            color: root.bar ? root.bar.urgent : Color.urgent
            opacity: 0.9
            font.pixelSize: Style.font.caption
            font.family: root.bar ? root.bar.fontFamily : Style.font.family
          }

          // Actions
          Row {
            width: parent.width
            spacing: Style.space(6)
            Text {
              text: root.connBusy ? "Testing\u2026" : "Test connection"
              color: Color.accent
              opacity: root.addrText.trim() !== "" && !root.connBusy ? 1.0 : 0.4
              font.pixelSize: Style.font.body
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
              MouseArea {
                anchors.fill: parent
                cursorShape: Qt.PointingHandCursor
                onClicked: if (!root.connBusy) root.runTest()
              }
            }
            Text {
              text: "Save"
              color: Color.accent
              opacity: root.addrText.trim() !== "" && !root.connBusy ? 1.0 : 0.4
              font.pixelSize: Style.font.body
              font.family: root.bar ? root.bar.fontFamily : Style.font.family
              MouseArea {
                anchors.fill: parent
                cursorShape: Qt.PointingHandCursor
                onClicked: if (!root.connBusy) root.saveConnection()
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
      // Watchdog first: a same-ref replay query (if issued) occupies
      // the one-in-flight slot and the status poll waits a tick.
      root.mutationWatchdog()
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
