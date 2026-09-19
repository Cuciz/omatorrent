// OmaTorrent mutation client state machine (IPC v1.2, ADR-0006).
//
// Pure presentation-side bookkeeping: request/terminal correlation for
// staged mutations. It understands ONLY the daemon's wire shapes
// (mutation.accepted / mutation.rejected / mutation.result); no
// qBittorrent vocabulary, no backend semantics. The daemon explicitly
// permits either frame order — a mutation.result push may be delivered
// BEFORE the matching mutation.accepted response (ADR-0006 amendment) —
// so this module makes responses and pushes genuinely order-independent:
//
//   - pending entries are keyed by torrent hash and correlate to their
//     terminal result by daemon mutation id once known;
//   - a terminal push whose mutation id is not yet known is buffered in
//     a BOUNDED early-result map keyed by mutation id;
//   - when mutation.accepted arrives and an early result already exists
//     for its mutation id, the terminal is applied immediately and NO
//     pending overlay is created;
//   - foreign mutation ids never cross-resolve (matching is strictly by
//     mutation id, never by action+hash guessing);
//   - duplicate/late terminal pushes are harmless (buffered, bounded,
//     eventually evicted).
//
// Consumed by plugins/local.omatorrent/Panel.qml (live client) and by
// tools/test_quickshell.sh (deterministic ordering tests import this
// exact file — the tests exercise the real logic, not a copy).
//
// All functions are pure with respect to the state object they are
// given; they return plain effect records the caller maps onto the UI.

var MAX_EARLY = 16

// newState creates the per-connection state bag.
//   pending:  hash -> {action, mutation?, ref, hash, url, deleteFiles, since, queried?}
//   early:    mutation id -> terminal result object (bounded, FIFO)
//   earlyOrder: mutation ids in insertion order (for bounded eviction)
//   inflight: {id, action, hash, url, deleteFiles, ref} of the one
//             outstanding request, or null (lockstep client discipline)
function newState() {
  return { pending: {}, early: {}, earlyOrder: [], inflight: null }
}

// begin records the intent at REQUEST time (one in flight). hash is ""
// for torrent.add (the daemon derives and reports it at acceptance).
function begin(st, id, action, hash, url, deleteFiles, ref, now) {
  if (st.inflight !== null) return false
  st.inflight = {
    id: id, action: action, hash: hash || "", url: url || "",
    deleteFiles: !!deleteFiles, ref: ref, since: now
  }
  // Overlays for hash-carrying actions appear immediately (cosmetic,
  // never authoritative). torrent.add gets its overlay only at
  // acceptance, when the daemon reports the parsed infohash.
  if (st.inflight.hash !== "") {
    st.pending[hash] = {
      action: action, ref: ref, hash: hash, url: "", deleteFiles: !!deleteFiles,
      since: now
    }
  }
  return true
}

// bufferEarly retains a terminal result for a not-yet-known mutation id
// (bounded; oldest evicted). Returns true when an entry was evicted.
function bufferEarly(st, msg) {
  if (st.early[msg.mutation] === undefined) {
    st.earlyOrder.push(msg.mutation)
    if (st.earlyOrder.length > MAX_EARLY) {
      delete st.early[st.earlyOrder.shift()]
    }
  }
  st.early[msg.mutation] = msg
}

// applyTerminal settles a terminal outcome: clears the pending overlay
// when it belongs to this mutation, and returns the applied record.
function applyTerminal(st, t) {
  var e = st.pending[t.hash]
  if (e && e.action === t.action) {
    if (e.mutation === undefined || e.mutation === t.mutation) {
      delete st.pending[t.hash]
    }
  }
  return { applied: true, action: t.action, hash: t.hash, status: t.status }
}

// onAccepted handles a mutation.accepted response.
//   {handled:false}            — stray/foreign id (lockstep mismatch)
//   {handled:true, applied:T}  — early terminal existed; applied now,
//                                NO pending overlay created
//   {handled:true, applied:null} — normal path; pending registered/updated
function onAccepted(st, msg) {
  if (st.inflight === null || msg.id !== st.inflight.id) {
    return { handled: false }
  }
  var req = st.inflight
  st.inflight = null

  var earlyMsg = st.early[msg.mutation]
  if (earlyMsg !== undefined) {
    // Result already arrived (legal B-order). Settle now; never create
    // a fresh overlay for an already-terminal mutation.
    delete st.early[msg.mutation]
    var i = st.earlyOrder.indexOf(msg.mutation)
    if (i >= 0) st.earlyOrder.splice(i, 1)
    return { handled: true, applied: applyTerminal(st, earlyMsg) }
  }

  if (msg.action === "torrent.add") {
    st.pending[msg.hash] = {
      action: msg.action, mutation: msg.mutation, ref: req.ref,
      hash: msg.hash, url: req.url, deleteFiles: false, since: req.since || 0
    }
  } else {
    var e = st.pending[msg.hash]
    if (e && e.action === msg.action) {
      e.mutation = msg.mutation
    } else {
      st.pending[msg.hash] = {
        action: msg.action, mutation: msg.mutation, ref: req.ref,
        hash: msg.hash, url: "", deleteFiles: req.deleteFiles,
        since: req.since || 0
      }
    }
  }
  return { handled: true, applied: null }
}

// onResultPush handles a server-pushed mutation.result (no id).
//   {applied:true, ...}       — matched a known pending mutation; its
//                               overlay is cleared
//   {applied:false, buffered} — mutation id unknown yet (early result)
//                               or foreign/duplicate (harmless)
function onResultPush(st, msg) {
  var e = st.pending[msg.hash]
  if (e && e.action === msg.action && e.mutation === msg.mutation) {
    delete st.pending[msg.hash]
    return { applied: true, action: msg.action, hash: msg.hash, status: msg.status }
  }
  bufferEarly(st, msg)
  return { applied: false, buffered: true }
}

// onResultResponse handles a mutation.result that carries an id — the
// replay answer to a same-ref query (or a replayed completed request):
// it is the terminal for the CURRENT request and settles it directly.
function onResultResponse(st, msg) {
  if (st.inflight === null || msg.id !== st.inflight.id) {
    return { handled: false }
  }
  st.inflight = null
  var out = applyTerminal(st, msg)
  // The mutation id may also be buffered early (push raced the replay):
  // consume it so it cannot leak as a stale early entry.
  if (st.early[msg.mutation] !== undefined) {
    delete st.early[msg.mutation]
    var i = st.earlyOrder.indexOf(msg.mutation)
    if (i >= 0) st.earlyOrder.splice(i, 1)
  }
  out.handled = true
  return out
}

// onRejected handles a mutation.rejected response — terminal for the
// attempt: the daemon will NOT perform it, so the overlay must go.
function onRejected(st, msg) {
  if (st.inflight === null || msg.id !== st.inflight.id) {
    return { handled: false }
  }
  var req = st.inflight
  st.inflight = null
  if (req.hash !== "" && st.pending[req.hash] !== undefined &&
      st.pending[req.hash].action === req.action) {
    delete st.pending[req.hash]
  }
  return { handled: true, code: msg.code, hash: req.hash }
}

// reset drops ALL correlation state (disconnect/reconnect: the fresh
// snapshot is the truth; pending overlays never survive a connection
// loss — ADR-0006 client rules).
function reset(st) {
  st.pending = {}
  st.early = {}
  st.earlyOrder = []
  st.inflight = null
}
