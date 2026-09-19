#!/usr/bin/env bash
# Isolated Quickshell IPC smoke test: proves the Quickshell.Io Socket +
# SplitParser client path against the running omatorrent-service daemon,
# without touching the user's shell.
#
# Mirrors the production client discipline (docs/IPC.md): ONE request in
# flight at any time across ALL request types (pendingKind), cleared only
# by the matching id-verified response — status first, subscribe only
# after the status response, subscribed set only on torrent.subscribed.
# Also runs a deterministic rename-ordering model check mirroring the
# panel's source model (map + sorted order + rename repositioning).
#
# v1.2 mutation flow (Phase 0.3): exercises the full staged contract
# against a DISPOSABLE magnet (random infohash that can never resolve
# metadata or create files; removed with both delete_files variants at
# the end). The user's real torrents are never mutated.
#
# Exit 0 = >=2 id-matched status exchanges + verified subscription + full
# snapshot + rename model OK + all mutation stages OK. Requires:
# omatorrent-service running (default socket), quickshell (qs).
set -euo pipefail

SOCK="${OT_SOCKET:-${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/omatorrent/service.sock}"
DIR="$(mktemp -d /tmp/ot-qs-test.XXXXXX)"
trap 'rm -rf "$DIR"' EXIT

cat > "$DIR/shell.qml" <<'QMLEOF'
import QtQuick
import Quickshell
import Quickshell.Io
import "MUTATIONCLIENT" as MC

ShellRoot {
  id: root
  property bool helloSent: false
  property int pendingId: -1
  property string pendingKind: ""
  property real pendingSince: 0
  property int nextId: 1
  property bool subscribed: false
  property int matched: 0
  property int snapshotBegin: 0
  property int snapshotItems: 0
  property int snapshotEnd: 0
  property bool renameTestDone: false
  property bool orderingTestDone: false

  // ---- v1.2 mutation stages (all against a disposable torrent).
  property string ghostHash: ""   // random 40-hex: never resolvable, no data
  property int mutStage: 0        // 0=waiting for snapshot; advance via mutStep()
  property string mutWait: ""     // waiting for a pushed result action
  property int mutPass: 0
  property int mutTotal: 10
  property string runId: ""
  property string addRef: ""

  function genHash() {
    var s = ""
    for (var i = 0; i < 40; i++) s += Math.floor(Math.random() * 16).toString(16)
    return s
  }

  function send(msg) { sock.write(JSON.stringify(msg) + "\n"); sock.flush() }

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

  function requestMutation(action, extra, refOverride) {
    if (pendingKind !== "" || !helloSent) return false
    pendingKind = "mutation"
    pendingId = nextId++
    pendingSince = Date.now()
    const ref = refOverride !== undefined ? refOverride : "smoke-" + runId + "-" + mutStage
    send(Object.assign({ type: action, id: pendingId, ref: ref }, extra))
    return true
  }

  function mutStageDone(ok, label) {
    if (!ok) {
      print("OTQS-FAIL mutation stage " + mutStage + " (" + label + ")")
      Qt.quit()
      return
    }
    print("OTQS-MUT stage " + mutStage + " ok (" + label + ")")
    mutPass++
    mutStage++
    mutStep()
  }

  // One stage at a time; stages expecting a pushed result set mutWait
  // and advance when the result frame arrives.
  function mutStep() {
    if (pendingKind !== "" || mutWait !== "") return
    const magnet = "magnet:?xt=urn:btih:" + ghostHash + "&dn=otqs-disposable-smoke"
    switch (mutStage) {
      case 0: // wait for snapshot (handled in snapshot.end)
        return
      case 1: // add disposable magnet -> accepted + confirmed result
        addRef = "smoke-" + runId + "-1"
        if (requestMutation("torrent.add", { url: magnet }, addRef)) mutWait = "torrent.add"
        break
      case 2: // duplicate add -> rejected duplicate
        if (requestMutation("torrent.add", { url: magnet })) mutWait = "!duplicate"
        break
      case 3: // replay the COMPLETED stage-1 ref -> result-with-id confirmed
        if (requestMutation("torrent.add", { url: magnet }, addRef)) mutWait = "!replay"
        break
      case 4: // structurally invalid magnet -> rejected invalid_url (no close)
        if (requestMutation("torrent.add", { url: "magnet:?xt=urn:btih:NOTHEX&dn=x" })) mutWait = "!invalid_url"
        break
      case 5: // pause -> accepted + confirmed
        if (requestMutation("torrent.pause", { hash: ghostHash })) mutWait = "torrent.pause"
        break
      case 6: // resume -> accepted + confirmed
        if (requestMutation("torrent.resume", { hash: ghostHash })) mutWait = "torrent.resume"
        break
      case 7: // remove WITHOUT files -> confirmed absent
        if (requestMutation("torrent.remove", { hash: ghostHash, delete_files: false })) mutWait = "torrent.remove"
        break
      case 8: // re-add -> confirmed present (fresh ref, new attempt)
        if (requestMutation("torrent.add", { url: magnet })) mutWait = "torrent.add"
        break
      case 9: // remove WITH files (torrent has none) -> confirmed absent
        if (requestMutation("torrent.remove", { hash: ghostHash, delete_files: true })) mutWait = "torrent.remove"
        break
      case 10: // unknown hash -> rejected stale_torrent
        if (requestMutation("torrent.pause", { hash: "ffffffffffffffffffffffffffffffffffffffff" })) mutWait = "!stale_torrent"
        break
      default:
        maybeDone()
    }
  }

  // Deterministic v1.2 frame-ordering tests against the REAL client
  // state machine (plugins/local.omatorrent/MutationClient.js is
  // imported above — this exercises the same file the panel uses).
  // ADR-0006 permits BOTH legal orders: mutation.accepted then
  // mutation.result, and mutation.result then mutation.accepted.
  function runOrderingTests() {
    const H = "0123456789abcdef0123456789abcdef01234567"
    const errs = []
    const acts = ["torrent.pause", "torrent.resume", "torrent.remove"]

    // A-order (accepted -> result) for hash actions.
    for (const act of acts) {
      const st = MC.newState()
      MC.begin(st, 1, act, H, "", act === "torrent.remove", "r1", 1000)
      if (st.pending[H] === undefined) errs.push(act + " A: no overlay at begin")
      let r = MC.onAccepted(st, { id: 1, mutation: 7, action: act, hash: H })
      if (!r.handled || r.applied !== null) errs.push(act + " A: accepted must register")
      r = MC.onResultPush(st, { mutation: 7, action: act, hash: H, status: "confirmed" })
      if (!r.applied || st.pending[H] !== undefined) errs.push(act + " A: push not applied")
    }

    // B-order (result -> accepted) for hash actions: the push is
    // buffered, the overlay survives until accepted applies the early
    // result — never recreated afterwards.
    for (const act of acts) {
      const st = MC.newState()
      MC.begin(st, 1, act, H, "", false, "r1", 1000)
      let r = MC.onResultPush(st, { mutation: 7, action: act, hash: H, status: "confirmed" })
      if (r.applied) errs.push(act + " B: push applied before correlation")
      if (st.pending[H] === undefined) errs.push(act + " B: overlay vanished early")
      r = MC.onAccepted(st, { id: 1, mutation: 7, action: act, hash: H })
      if (!r.handled || !r.applied) errs.push(act + " B: early result not applied")
      if (st.pending[H] !== undefined) errs.push(act + " B: overlay recreated by accepted")
    }

    // A-order add.
    {
      const st = MC.newState()
      const url = "magnet:?xt=urn:btih:" + H
      MC.begin(st, 1, "torrent.add", "", url, false, "r1", 1000)
      if (st.pending[H] !== undefined) errs.push("add A: overlay before accepted")
      let r = MC.onAccepted(st, { id: 1, mutation: 9, action: "torrent.add", hash: H })
      if (!r.handled || r.applied !== null) errs.push("add A: accepted must register")
      if (st.pending[H] === undefined || st.pending[H].mutation !== 9) errs.push("add A: not registered")
      r = MC.onResultPush(st, { mutation: 9, action: "torrent.add", hash: H, status: "confirmed" })
      if (!r.applied || st.pending[H] !== undefined) errs.push("add A: push not applied")
    }

    // B-order add — THE external-review case: result before accepted
    // must never leave a stale pending overlay.
    {
      const st = MC.newState()
      const url = "magnet:?xt=urn:btih:" + H
      MC.begin(st, 1, "torrent.add", "", url, false, "r1", 1000)
      let r = MC.onResultPush(st, { mutation: 9, action: "torrent.add", hash: H, status: "confirmed" })
      if (r.applied) errs.push("add B: push applied without correlation")
      if (st.pending[H] !== undefined) errs.push("add B: overlay created by push")
      r = MC.onAccepted(st, { id: 1, mutation: 9, action: "torrent.add", hash: H })
      if (!r.handled || !r.applied) errs.push("add B: early result not applied at accepted")
      if (st.pending[H] !== undefined) errs.push("add B: STUCK OVERLAY after accepted")
      if (st.early[9] !== undefined) errs.push("add B: early entry not consumed")
    }

    // Disconnect/reset clears everything; a post-reset accepted is a
    // stray and must create no state.
    {
      const st = MC.newState()
      MC.begin(st, 1, "torrent.add", "", "magnet:?xt=urn:btih:" + H, false, "r1", 1000)
      MC.onResultPush(st, { mutation: 9, action: "torrent.add", hash: H, status: "confirmed" })
      MC.reset(st)
      if (Object.keys(st.pending).length || Object.keys(st.early).length || st.inflight !== null)
        errs.push("reset did not clear state")
      const r = MC.onAccepted(st, { id: 1, mutation: 9, action: "torrent.add", hash: H })
      if (r.handled || st.pending[H] !== undefined) errs.push("post-reset accepted created state")
    }

    // Unrelated mutation ids never cross-resolve.
    {
      const st = MC.newState()
      MC.begin(st, 1, "torrent.pause", H, "", false, "r1", 1000)
      MC.onAccepted(st, { id: 1, mutation: 7, action: "torrent.pause", hash: H })
      const r = MC.onResultPush(st, { mutation: 99, action: "torrent.pause", hash: H, status: "confirmed" })
      if (r.applied || st.pending[H] === undefined) errs.push("foreign id cross-resolved")
      const r2 = MC.onResultPush(st, { mutation: 7, action: "torrent.pause", hash: H, status: "confirmed" })
      if (!r2.applied || st.pending[H] !== undefined) errs.push("own id not applied")
    }

    // Duplicate/late result is harmless.
    {
      const st = MC.newState()
      MC.begin(st, 1, "torrent.pause", H, "", false, "r1", 1000)
      MC.onAccepted(st, { id: 1, mutation: 7, action: "torrent.pause", hash: H })
      MC.onResultPush(st, { mutation: 7, action: "torrent.pause", hash: H, status: "confirmed" })
      const r = MC.onResultPush(st, { mutation: 7, action: "torrent.pause", hash: H, status: "confirmed" })
      if (r.applied) errs.push("duplicate push applied twice")
      if (st.pending[H] !== undefined) errs.push("duplicate push recreated overlay")
    }

    // Early-result buffer is bounded (FIFO eviction).
    {
      const st = MC.newState()
      for (let i = 1; i <= 20; i++)
        MC.onResultPush(st, { mutation: i, action: "torrent.add", hash: "h" + i, status: "confirmed" })
      const n = Object.keys(st.early).length
      if (n > 16) errs.push("early buffer unbounded: " + n)
      if (st.early[1] !== undefined || st.early[4] !== undefined) errs.push("FIFO eviction broken")
      if (st.early[20] === undefined) errs.push("newest early entry lost")
    }

    // A same-ref watchdog query settles via the replay response.
    {
      const st = MC.newState()
      MC.begin(st, 1, "torrent.pause", H, "", false, "r1", 1000)
      MC.onAccepted(st, { id: 1, mutation: 7, action: "torrent.pause", hash: H })
      MC.begin(st, 2, "torrent.pause", H, "", false, "r1", 2000)
      const r = MC.onResultResponse(st, { id: 2, mutation: 7, action: "torrent.pause", hash: H, status: "confirmed" })
      if (!r.handled || !r.applied || st.pending[H] !== undefined || st.inflight !== null)
        errs.push("replay response did not settle the query")
    }

    if (errs.length) {
      print("OTQS-FAIL ordering: " + errs.join("; "))
      return
    }
    print("OTQS-ORDERING-OK")
    orderingTestDone = true
  }

  // Deterministic rename-ordering check mirroring the panel's source
  // model (map + sorted order + rename repositioning).
  function runRenameModelTest() {
    var torrents = ({})
    var order = []
    function insertSorted(hash, item) {
      var lo = 0, hi = order.length
      while (lo < hi) {
        var mid = (lo + hi) >> 1
        if (torrents[order[mid]].name < item.name) lo = mid + 1
        else hi = mid
      }
      order.splice(lo, 0, hash)
    }
    torrents["hA"] = { hash: "hA", name: "Alpha torrent" }
    torrents["hB"] = { hash: "hB", name: "Zeta torrent" }
    insertSorted("hA", torrents["hA"])
    insertSorted("hB", torrents["hB"])
    if (order[0] !== "hA" || order[1] !== "hB") {
      print("OTQS-FAIL rename-model initial order " + order)
      return
    }
    // Rename B so it sorts BEFORE A ("Aardvark" < "Alpha"); the model
    // must reposition it.
    torrents["hB"] = { hash: "hB", name: "Aardvark torrent" }
    var i = order.indexOf("hB")
    if (i >= 0) order.splice(i, 1)
    insertSorted("hB", torrents["hB"])
    if (order[0] !== "hB" || order[1] !== "hA") {
      print("OTQS-FAIL rename order after rename " + order)
      return
    }
    print("OTQS-RENAME-OK")
    renameTestDone = true
  }

  function handle(line) {
    let msg
    try { msg = JSON.parse(line) } catch (e) { return }
    if (!msg || typeof msg.type !== "string") return
    switch (msg.type) {
      case "hello":
        requestStatus() // subscribe only AFTER the status completes
        break
      case "system.status":
        if (pendingKind !== "status" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingKind = ""
        pendingId = -1
        matched++
        print("OTQS-READ: " + line)
        if (!subscribed) requestSubscribe()
        break
      case "torrent.subscribed":
        if (pendingKind !== "subscribe" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingKind = ""
        pendingId = -1
        subscribed = true
        print("OTQS-SUBSCRIBED")
        break
      case "torrent.snapshot.begin":
        snapshotBegin++
        break
      case "torrent.snapshot.item":
        if (msg.torrent && typeof msg.torrent.hash === "string") snapshotItems++
        break
      case "torrent.snapshot.end":
        snapshotEnd++
        print("OTQS-SNAPSHOT items=" + snapshotItems)
        if (runId === "") runId = String(Date.now())
        ghostHash = genHash()
        mutStage = 1
        mutStep()
        maybeDone()
        break
      case "mutation.accepted":
        if (pendingKind !== "mutation" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingKind = ""
        pendingId = -1
        // Accepted = submitted, not success; stages that expect a code or
        // a replay resolve here, confirmed-result stages wait for the push.
        if (mutWait === "!replay") {
          mutWait = ""
          mutStageDone(false, "replay answered accepted instead of result")
        }
        break
      case "mutation.rejected":
        if (pendingKind !== "mutation" || typeof msg.id !== "number" || msg.id !== pendingId) return
        pendingKind = ""
        pendingId = -1
        if (mutWait === "!" + msg.code) {
          const code = msg.code
          mutWait = ""
          mutStageDone(true, "rejected " + code)
        } else {
          print("OTQS-FAIL stage " + mutStage + " rejected " + msg.code + " wanted " + mutWait)
          Qt.quit()
        }
        break
      case "mutation.result":
        if (typeof msg.id === "number") {
          // Replay answer carries the request id.
          if (pendingKind !== "mutation" || msg.id !== pendingId) return
          pendingKind = ""
          pendingId = -1
          if (mutWait === "!replay") {
            const ok = msg.status === "confirmed"
            mutWait = ""
            mutStageDone(ok, "replay result " + msg.status)
          }
          break
        }
        // Pushed terminal result: must be confirmed for the waiting stage.
        if (mutWait === msg.action && msg.hash === ghostHash) {
          const st = msg.status
          mutWait = ""
          mutStageDone(st === "confirmed", msg.action + " " + st)
        }
        break
      case "torrent.delta":
      case "error":
        break
    }
  }

  function maybeDone() {
    if (matched >= 2 && snapshotBegin === 1 && snapshotEnd === 1 && renameTestDone && orderingTestDone && mutPass >= mutTotal) {
      print("OTQS-DONE")
      Qt.quit()
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
      if (!root.renameTestDone) root.runRenameModelTest()
      if (!root.orderingTestDone) root.runOrderingTests()
      root.mutStep()
      root.maybeDone()
      if (!root.helloSent && sock.connected) {
        root.helloSent = true
        root.send({ type: "hello", protocol: 1 })
        print("OTQS-SENT hello")
      } else if (root.helloSent && root.pendingKind === "") {
        root.requestStatus()
      } else if (root.helloSent && root.pendingKind !== "" && Date.now() - root.pendingSince > 8000) {
        print("OTQS-FAIL stuck pending " + root.pendingKind)
        Qt.quit()
      }
    }
  }
  Timer { interval: 90000; running: true; onTriggered: { print("OTQS-FAIL hard timeout (stage " + mutStage + " wait " + mutWait + ")"); Qt.quit() } } // hard timeout
}
QMLEOF
MJS="$(cd "$(dirname "$0")/.." && pwd)/plugins/local.omatorrent/MutationClient.js"
sed -i "s|SOCKPATH|${SOCK}|g; s|MUTATIONCLIENT|${MJS}|g" "$DIR/shell.qml"

echo "== isolated quickshell IPC smoke against $SOCK =="
timeout 100 qs -p "$DIR" 2>&1 | grep -E "OTQS-" | tee "$DIR/out.log" >&2

status=$(grep -c 'OTQS-READ: {"type":"system.status"' "$DIR/out.log" || true)
subd=$(grep -c 'OTQS-SUBSCRIBED' "$DIR/out.log" || true)
snap=$(grep -c 'OTQS-SNAPSHOT' "$DIR/out.log" || true)
rename=$(grep -c 'OTQS-RENAME-OK' "$DIR/out.log" || true)
ordering=$(grep -c 'OTQS-ORDERING-OK' "$DIR/out.log" || true)
mut=$(grep -c 'OTQS-MUT stage' "$DIR/out.log" || true)
fail=$(grep -c 'OTQS-FAIL' "$DIR/out.log" || true)
doneMarker=$(grep -c 'OTQS-DONE' "$DIR/out.log" || true)

echo "matched-status=$status subscribed=$subd snapshots=$snap rename-model=$rename ordering=$ordering mutation-stages=$mut/10 failures=$fail"
if [ "$fail" -eq 0 ] && [ "$doneMarker" -ge 1 ] && [ "$subd" -ge 1 ] && [ "$rename" -ge 1 ] && [ "$ordering" -ge 1 ] && [ "$mut" -ge 10 ]; then
  echo "PASS: one-in-flight discipline + v1.1 snapshot + rename model + v1.2 mutation lifecycle (disposable torrent, both delete_files paths, duplicate/replay/invalid/stale rejections) + deterministic frame-ordering tests (both legal orders, all actions)"
  exit 0
fi
echo "FAIL: expected OTQS-DONE with subscribed + rename-model + 10 mutation stages and no OTQS-FAIL"
exit 1
