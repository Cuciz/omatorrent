// Package ipc implements the OmaTorrent IPC v1 server (docs/IPC.md,
// ADR-0004). Messages are flat JSON objects framed by LF (max 4096 bytes
// including LF). The grammar is strict: unknown fields, duplicate keys,
// null values, non-integer numbers where integers are required, invalid
// UTF-8 and trailing JSON are all invalid_message. Error responses never
// echo payload or ids.
package ipc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxFrame is the maximum frame size including the trailing LF.
const MaxFrame = 4096

// Error codes (docs/IPC.md).
const (
	CodeInvalidMessage    = "invalid_message"
	CodeMessageTooLarge   = "message_too_large"
	CodeHandshakeRequired = "handshake_required"
	CodeVersionMismatch   = "version_mismatch"
	CodeUnsupported       = "unsupported_message"
)

// ProtocolVersion is the only accepted protocol major version.
const ProtocolVersion = 1

// ServiceName identifies the daemon in the hello response.
const ServiceName = "omatorrent-service"

// Request is a parsed client frame.
type Request struct {
	Type        string
	Protocol    int64
	ID          int64
	Hash        string
	URL         string
	DeleteFiles bool
	Ref         string
}

// Response frames. Field order matches docs/IPC.md (encoding/json emits
// struct fields in declaration order).

type helloResponse struct {
	Type     string `json:"type"`
	Protocol int64  `json:"protocol"`
	Service  string `json:"service"`
}

type healthResponse struct {
	Type     string `json:"type"`
	Protocol int64  `json:"protocol"`
	ID       int64  `json:"id"`
	Service  string `json:"service"`
	Backend  string `json:"backend"`
}

type statusOKResponse struct {
	Type          string `json:"type"`
	Protocol      int64  `json:"protocol"`
	ID            int64  `json:"id"`
	QBittorrent   string `json:"qbittorrent"`
	AppVersion    string `json:"app_version"`
	WebAPIVersion string `json:"webapi_version"`
	DlSpeed       int64  `json:"dl_speed"`
	UpSpeed       int64  `json:"up_speed"`
	TorrentsTotal int    `json:"torrents_total"`
}

type statusUnavailableResponse struct {
	Type        string `json:"type"`
	Protocol    int64  `json:"protocol"`
	ID          int64  `json:"id"`
	QBittorrent string `json:"qbittorrent"`
}

type errorResponse struct {
	Type     string `json:"type"`
	Protocol int64  `json:"protocol"`
	Code     string `json:"code"`
}

// ---- v1.1 torrent state delivery (ADR-0005) ----

// TorrentItem is the normalized torrent wire item (exact key set,
// identical in snapshot items and deltas). Name is capped at
// nameCapRunes runes by the encoder — the frame budget rule.
type TorrentItem struct {
	Hash      string  `json:"hash"`
	Name      string  `json:"name"`
	State     string  `json:"state"`
	Progress  float64 `json:"progress"`
	DlSpeed   int64   `json:"dlspeed"`
	UpSpeed   int64   `json:"upspeed"`
	Eta       int64   `json:"eta"`
	Ratio     float64 `json:"ratio"`
	Category  string  `json:"category"`
	Size      int64   `json:"size"`
	Completed int64   `json:"completed"`
}

// NameCapRunes bounds torrent names on the wire (ADR-0005).
const NameCapRunes = 512

type subscribedResponse struct {
	Type     string `json:"type"`
	Protocol int64  `json:"protocol"`
	ID       int64  `json:"id"`
}

type snapshotBeginResponse struct {
	Type     string `json:"type"`
	Protocol int64  `json:"protocol"`
	ID       int64  `json:"id"`
	Count    int    `json:"count"`
}

type snapshotItemResponse struct {
	Type     string      `json:"type"`
	Protocol int64       `json:"protocol"`
	ID       int64       `json:"id"`
	Index    int         `json:"index"`
	Torrent  TorrentItem `json:"torrent"`
}

type snapshotEndResponse struct {
	Type     string `json:"type"`
	Protocol int64  `json:"protocol"`
	ID       int64  `json:"id"`
}

type deltaResponse struct {
	Type     string        `json:"type"`
	Protocol int64         `json:"protocol"`
	Seq      uint64        `json:"seq"`
	Changed  []TorrentItem `json:"changed"`
	Removed  []string      `json:"removed"`
}

// ---- v1.2 staged mutations (ADR-0006) ----

type mutationAcceptedResponse struct {
	Type     string `json:"type"`
	Protocol int64  `json:"protocol"`
	ID       int64  `json:"id"`
	Mutation uint64 `json:"mutation"`
	Action   string `json:"action"`
	Hash     string `json:"hash"`
}

type mutationRejectedResponse struct {
	Type     string `json:"type"`
	Protocol int64  `json:"protocol"`
	ID       int64  `json:"id"`
	Code     string `json:"code"`
}

type mutationResultResponse struct {
	Type     string `json:"type"`
	Protocol int64  `json:"protocol"`
	Mutation uint64 `json:"mutation"`
	Action   string `json:"action"`
	Hash     string `json:"hash"`
	Status   string `json:"status"`
}

// mutationResultIDResponse is the replay shape: a completed ref
// replayed on a NEW request answers with the recorded terminal result,
// correlated to that request's id.
type mutationResultIDResponse struct {
	Type     string `json:"type"`
	Protocol int64  `json:"protocol"`
	ID       int64  `json:"id"`
	Mutation uint64 `json:"mutation"`
	Action   string `json:"action"`
	Hash     string `json:"hash"`
	Status   string `json:"status"`
}

// ---- v1.3 dashboard aggregates (ADR-0007) ----

// DashboardCounts is the torrent population summary (classification
// semantics pinned by ADR-0007; counts are not expected to sum to
// total — queued/checking/error/moving/other are inside total only).
type DashboardCounts struct {
	Total       int `json:"total"`
	Active      int `json:"active"`
	Downloading int `json:"downloading"`
	Seeding     int `json:"seeding"`
	Paused      int `json:"paused"`
	Completed   int `json:"completed"`
}

// DashboardAggregate is the current data summary in bytes (saturating
// sums computed by the state layer).
type DashboardAggregate struct {
	TotalSize      int64 `json:"total_size"`
	CompletedBytes int64 `json:"completed_bytes"`
	RemainingBytes int64 `json:"remaining_bytes"`
}

// dashboardActiveItem is one entry of the "transferring now" list:
// current state only, never history.
type dashboardActiveItem struct {
	Name     string  `json:"name"`
	State    string  `json:"state"`
	Progress float64 `json:"progress"`
	DlSpeed  int64   `json:"dlspeed"`
	UpSpeed  int64   `json:"upspeed"`
}

// DashboardActiveItem is the state-layer-mirrored active entry (main
// adapts state.DashboardActiveItem to these).
type DashboardActiveItem struct {
	Name     string
	State    string
	Progress float64
	DlSpeed  int64
	UpSpeed  int64
}

// dashboardStatusOKResponse is the live shape. free_space is present
// iff the backend has reported it (pointer, so 0 bytes is a real value;
// nil means unknown and is omitted — ADR-0007).
type dashboardStatusOKResponse struct {
	Type          string                `json:"type"`
	Protocol      int64                 `json:"protocol"`
	ID            int64                 `json:"id"`
	QBittorrent   string                `json:"qbittorrent"`
	AppVersion    string                `json:"app_version"`
	WebAPIVersion string                `json:"webapi_version"`
	DlSpeed       int64                 `json:"dl_speed"`
	UpSpeed       int64                 `json:"up_speed"`
	FreeSpace     *int64                `json:"free_space,omitempty"`
	Counts        DashboardCounts       `json:"counts"`
	Aggregate     DashboardAggregate    `json:"aggregate"`
	Active        []dashboardActiveItem `json:"active"`
}

// dashboardLastKnown carries last-known-good counts/aggregate/versions
// while the backend is unreachable (speeds and the active list are NOT
// included: unknown, and fake zeroes are forbidden).
type dashboardLastKnown struct {
	AppVersion    string             `json:"app_version"`
	WebAPIVersion string             `json:"webapi_version"`
	Counts        DashboardCounts    `json:"counts"`
	Aggregate     DashboardAggregate `json:"aggregate"`
}

// dashboardStatusDegradedResponse is the degraded shape; last_known is
// present iff at least one sync cycle ever committed.
type dashboardStatusDegradedResponse struct {
	Type        string              `json:"type"`
	Protocol    int64               `json:"protocol"`
	ID          int64               `json:"id"`
	QBittorrent string              `json:"qbittorrent"`
	LastKnown   *dashboardLastKnown `json:"last_known,omitempty"`
}

// DashboardLastKnownData is the degraded-shape payload.
type DashboardLastKnownData struct {
	AppVersion    string
	WebAPIVersion string
	Counts        DashboardCounts
	Aggregate     DashboardAggregate
}

// DashboardData is the v1.3 payload the handler supplies, computed from
// committed state (ADR-0007). ok=false serves the degraded shape;
// LastKnown != nil adds the last-known-good object.
type DashboardData struct {
	AppVersion    string
	WebAPIVersion string
	DlSpeed       int64
	UpSpeed       int64
	FreeSpace     *int64
	Counts        DashboardCounts
	Aggregate     DashboardAggregate
	Active        []DashboardActiveItem
	LastKnown     *DashboardLastKnownData
}

// Active-name wire cap and the frame budget headroom for the ok shape
// (ADR-0007: names capped at 48 runes; pathological names halve, then
// trailing active entries drop — counts.active stays truthful).
const (
	dashboardActiveNameCapRunes = 48
	dashboardFrameBudget        = 3800
)

// EncodeDashboardStatus encodes the v1.3 response. The degraded shape
// carries no names and always fits; the ok shape is guarded against the
// frame budget by name-halving and then trailing-entry drops.
func EncodeDashboardStatus(id int64, ok bool, d DashboardData) []byte {
	if !ok {
		resp := dashboardStatusDegradedResponse{
			Type: "dashboard.status", Protocol: ProtocolVersion, ID: id,
			QBittorrent: "unavailable",
		}
		if d.LastKnown != nil {
			resp.LastKnown = &dashboardLastKnown{
				AppVersion:    d.LastKnown.AppVersion,
				WebAPIVersion: d.LastKnown.WebAPIVersion,
				Counts:        d.LastKnown.Counts,
				Aggregate:     d.LastKnown.Aggregate,
			}
		}
		return mustMarshal(resp)
	}

	nameCap := dashboardActiveNameCapRunes
	items := make([]dashboardActiveItem, len(d.Active))
	for {
		for i, it := range d.Active {
			items[i] = dashboardActiveItem{
				Name:     capString(it.Name, nameCap),
				State:    it.State,
				Progress: it.Progress,
				DlSpeed:  it.DlSpeed,
				UpSpeed:  it.UpSpeed,
			}
		}
		cur := items[:len(items)]
		if len(cur) == 0 {
			// Always a JSON array, never null.
			cur = []dashboardActiveItem{}
		}
		b := mustMarshal(dashboardStatusOKResponse{
			Type: "dashboard.status", Protocol: ProtocolVersion, ID: id,
			QBittorrent: "ok",
			AppVersion:  d.AppVersion, WebAPIVersion: d.WebAPIVersion,
			DlSpeed: d.DlSpeed, UpSpeed: d.UpSpeed,
			FreeSpace: d.FreeSpace,
			Counts:    d.Counts, Aggregate: d.Aggregate,
			Active: cur,
		})
		if len(b)+1 <= MaxFrame {
			return b
		}
		if nameCap > 12 {
			nameCap /= 2
			continue
		}
		if len(items) == 0 {
			return b // unreachable: the base shape fits the budget
		}
		items = items[:len(items)-1]
	}
}

// MutationRequest is one mutation intent from a client (already
// schema-validated by parseFrame).
type MutationRequest struct {
	Action      string
	Hash        string
	URL         string
	DeleteFiles bool
	Ref         string
}

// MutationResult is a terminal, state-derived mutation outcome.
type MutationResult struct {
	Mutation uint64
	Action   string
	Hash     string
	Status   string
}

// MutationStage1 is the synchronous answer to a mutation request:
// Outcome "accepted" or a rejection code (docs/IPC.md v1.2); Replay is
// non-nil when a completed ref was replayed — the recorded terminal
// result answers the request directly (with the current request id).
type MutationStage1 struct {
	Outcome  string
	Mutation uint64
	Action   string
	Hash     string
	Replay   *MutationResult
}

// Mutations supplies the v1.2 mutation surface. Submit may block up to
// a bounded backend-submission timeout; Results broadcasts terminal
// outcomes. Both must be safe for concurrent use.
type Mutations interface {
	Submit(r MutationRequest) MutationStage1
	Results() (<-chan MutationResult, func())
}

func EncodeMutationAccepted(id int64, mutation uint64, action, hash string) []byte {
	return mustMarshal(mutationAcceptedResponse{"mutation.accepted", ProtocolVersion, id, mutation, action, hash})
}

func EncodeMutationRejected(id int64, code string) []byte {
	return mustMarshal(mutationRejectedResponse{"mutation.rejected", ProtocolVersion, id, code})
}

// EncodeMutationResult encodes a pushed terminal result (no id: not a
// request response).
func EncodeMutationResult(r MutationResult) []byte {
	return mustMarshal(mutationResultResponse{"mutation.result", ProtocolVersion, r.Mutation, r.Action, r.Hash, r.Status})
}

// EncodeMutationResultWithID encodes a replayed terminal result as the
// response to the replaying request (carries that request's id).
func EncodeMutationResultWithID(id int64, r MutationResult) []byte {
	return mustMarshal(mutationResultIDResponse{"mutation.result", ProtocolVersion, id, r.Mutation, r.Action, r.Hash, r.Status})
}

// DeltaEvent is one committed change from the state source.
type DeltaEvent struct {
	Seq     uint64
	Changed []TorrentItem
	Removed []string
}

// CapName truncates a torrent name to the wire cap, marking truncation.
func CapName(name string) string {
	runes := []rune(name)
	if len(runes) <= NameCapRunes {
		return name
	}
	return string(runes[:NameCapRunes-1]) + "…"
}

func EncodeSubscribed(id int64) []byte {
	return mustMarshal(subscribedResponse{"torrent.subscribed", ProtocolVersion, id})
}

func EncodeSnapshotBegin(id int64, count int) []byte {
	return mustMarshal(snapshotBeginResponse{"torrent.snapshot.begin", ProtocolVersion, id, count})
}

func EncodeSnapshotEnd(id int64) []byte {
	return mustMarshal(snapshotEndResponse{"torrent.snapshot.end", ProtocolVersion, id})
}

// EncodeDeltas splits one change event into as many delta frames as the
// byte budget requires (all sharing seq; bounded per frame). Lists are
// always JSON arrays, never null. ok is false when a committed item
// cannot be framed within the budget — a committed update must never
// silently disappear, so the caller terminates the subscriber (which
// reconnects and rebuilds from a fresh snapshot) instead of dropping it.
func EncodeDeltas(ev DeltaEvent) (frames [][]byte, ok bool) {
	const budget = 3800 // headroom under MaxFrame for JSON overhead
	changed := make([]TorrentItem, 0, len(ev.Changed))
	removed := make([]string, 0, len(ev.Removed))
	flush := func() {
		if len(changed) == 0 && len(removed) == 0 {
			return
		}
		frames = append(frames, mustMarshal(deltaResponse{"torrent.delta", ProtocolVersion, ev.Seq, changed, removed}))
		changed = make([]TorrentItem, 0, cap(changed))
		removed = make([]string, 0, cap(removed))
	}
	size := 0
	for _, t := range ev.Changed {
		t.Name = CapName(t.Name)
		b, err := json.Marshal(t)
		if err != nil {
			return nil, false // unreachable for current field types
		}
		if len(b) > budget {
			return nil, false // never silently drop a committed update
		}
		if size+len(b) > budget && (len(changed) > 0 || len(removed) > 0) {
			flush()
			size = 0
		}
		changed = append(changed, t)
		size += len(b)
	}
	for _, h := range ev.Removed {
		if len(h) > 64 {
			return nil, false // defense in depth: hashes are 40/64 hex
		}
		if size+len(h)+16 > budget && (len(changed) > 0 || len(removed) > 0) {
			flush()
			size = 0
		}
		removed = append(removed, h)
		size += len(h) + 16
	}
	flush()
	return frames, true
}

// EncodeSnapshotItem caps name and category and never emits a frame
// above MaxFrame: with syncer-validated hashes (40/64 hex) the worst
// case fits the budget, and the halving guard below is pure defense
// against pathological escape amplification. A nil return means the
// item cannot be framed at all — the caller aborts the subscription
// (no silent loss).
func EncodeSnapshotItem(id int64, index int, t TorrentItem) []byte {
	t.Name = CapName(t.Name)
	t.Category = capString(t.Category, 128)
	b := mustMarshal(snapshotItemResponse{"torrent.snapshot.item", ProtocolVersion, id, index, t})
	for len(b)+1 > MaxFrame && len([]rune(t.Name)) > 32 {
		t.Name = capString(t.Name, len([]rune(t.Name))/2)
		b = mustMarshal(snapshotItemResponse{"torrent.snapshot.item", ProtocolVersion, id, index, t})
	}
	if len(b)+1 > MaxFrame {
		return nil
	}
	return b
}

func capString(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes-1]) + "…"
}

// EncodeHello, EncodeHealth, EncodeStatus, EncodeError produce response
// frames (without the trailing LF).

func EncodeHello() []byte {
	return mustMarshal(helloResponse{"hello", ProtocolVersion, ServiceName})
}

func EncodeHealth(id int64, backendOK bool) []byte {
	backend := "unavailable"
	if backendOK {
		backend = "ok"
	}
	return mustMarshal(healthResponse{"health", ProtocolVersion, id, "ready", backend})
}

// StatusData is the snapshot payload the handler supplies.
type StatusData struct {
	AppVersion    string
	WebAPIVersion string
	DlSpeed       int64
	UpSpeed       int64
	TorrentsTotal int
}

func EncodeStatus(id int64, ok bool, d StatusData) []byte {
	if !ok {
		return mustMarshal(statusUnavailableResponse{"system.status", ProtocolVersion, id, "unavailable"})
	}
	return mustMarshal(statusOKResponse{
		Type: "system.status", Protocol: ProtocolVersion, ID: id,
		QBittorrent: "ok", AppVersion: d.AppVersion, WebAPIVersion: d.WebAPIVersion,
		DlSpeed: d.DlSpeed, UpSpeed: d.UpSpeed, TorrentsTotal: d.TorrentsTotal,
	})
}

func EncodeError(code string) []byte {
	return mustMarshal(errorResponse{"error", ProtocolVersion, code})
}

func mustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		// Response types are fixed and always marshalable.
		panic(fmt.Sprintf("ipc: marshal response: %v", err))
	}
	return b
}

var errInvalid = sentinelErr{CodeInvalidMessage}

// hashRe and refRe validate v1.2 mutation fields at parse time
// (docs/IPC.md). The hash contract matches the state layer's
// normalization (40/64 hex, BitTorrent v1/v2 infohash).
var (
	hashRe = regexp.MustCompile(`^[0-9a-fA-F]{40}(?:[0-9a-fA-F]{24})?$`)
	refRe  = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

func validHash(h string) bool { return hashRe.MatchString(h) }
func validRef(r string) bool  { return refRe.MatchString(r) }

// Classified sentinels used across the package; failCode maps any error
// to its wire code.
var (
	errTooLarge    = sentinelErr{CodeMessageTooLarge}
	errHandshake   = sentinelErr{CodeHandshakeRequired}
	errVersion     = sentinelErr{CodeVersionMismatch}
	errUnsupported = sentinelErr{CodeUnsupported}
)

type sentinelErr struct{ code string }

func (e sentinelErr) Error() string { return "ipc: " + e.code }

func failCode(err error) string {
	var se sentinelErr
	if errors.As(err, &se) {
		return se.code
	}
	return CodeInvalidMessage
}

// parseFrame validates one frame (without LF) against the v1 grammar and
// returns the classified request. Schema errors are errInvalid
// (invalid_message); version/handshake/unsupported classification is left
// to the connection state machine.
func parseFrame(frame []byte) (Request, error) {
	if !utf8.Valid(frame) {
		return Request{}, errInvalid
	}

	dec := json.NewDecoder(bytes.NewReader(frame))
	dec.UseNumber()

	tok, err := dec.Token()
	if err != nil {
		return Request{}, errInvalid
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return Request{}, errInvalid
	}

	var (
		req                  Request
		hasType, hasProtocol bool
		hasID                bool
		hasHash, hasURL      bool
		hasDelete, hasRef    bool
	)
	seen := map[string]bool{}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return Request{}, errInvalid
		}
		key, ok := keyTok.(string)
		if !ok {
			return Request{}, errInvalid
		}
		if seen[key] {
			return Request{}, errInvalid // duplicate key
		}
		seen[key] = true

		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			return Request{}, errInvalid
		}
		if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
			return Request{}, errInvalid
		}

		decodeStr := func() (string, bool) {
			if len(raw) == 0 || raw[0] != '"' {
				return "", false
			}
			var s string
			if json.Unmarshal(raw, &s) != nil {
				return "", false
			}
			return s, true
		}

		switch key {
		case "type":
			s, ok := decodeStr()
			if !ok {
				return Request{}, errInvalid
			}
			req.Type, hasType = s, true
		case "protocol", "id":
			if len(raw) == 0 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) {
				return Request{}, errInvalid // must be a JSON number literal
			}
			var n json.Number
			if json.Unmarshal(raw, &n) != nil {
				return Request{}, errInvalid
			}
			i, err := strconv.ParseInt(n.String(), 10, 64)
			if err != nil {
				return Request{}, errInvalid // non-integer number
			}
			if key == "protocol" {
				req.Protocol, hasProtocol = i, true
			} else {
				req.ID, hasID = i, true
			}
		case "hash":
			s, ok := decodeStr()
			if !ok || !validHash(s) {
				return Request{}, errInvalid
			}
			req.Hash, hasHash = s, true
		case "url":
			s, ok := decodeStr()
			if !ok || len(s) == 0 || len(s) > 2048 || !strings.HasPrefix(s, "magnet:") {
				return Request{}, errInvalid
			}
			req.URL, hasURL = s, true
		case "delete_files":
			// REQUIRED JSON boolean literal on torrent.remove; anything
			// else (string "true", 1/0, null) is a protocol violation —
			// destructive ambiguity must never pass (ADR-0006).
			if !bytes.Equal(raw, []byte("true")) && !bytes.Equal(raw, []byte("false")) {
				return Request{}, errInvalid
			}
			req.DeleteFiles, hasDelete = string(raw) == "true", true
		case "ref":
			s, ok := decodeStr()
			if !ok || !validRef(s) {
				return Request{}, errInvalid
			}
			req.Ref, hasRef = s, true
		default:
			return Request{}, errInvalid // unknown field
		}
	}
	if !hasType {
		return Request{}, errInvalid
	}
	if tok, err := dec.Token(); err != nil {
		return Request{}, errInvalid
	} else if d, ok := tok.(json.Delim); !ok || d != '}' {
		return Request{}, errInvalid
	}
	if _, err := dec.Token(); err != io.EOF {
		return Request{}, errInvalid // trailing JSON
	}

	// Per-type exact key sets. Pre-v1.2 types must reject the v1.2 fields
	// too — a smuggled hash/url/delete_files/ref on health/status/
	// subscribe is invalid_message exactly as any other unknown field
	// was before v1.2 existed (review finding: grammar strictness).
	switch req.Type {
	case "hello":
		if !hasProtocol || hasID || hasHash || hasURL || hasDelete || hasRef {
			return Request{}, errInvalid
		}
	case "health", "system.status", "torrent.subscribe", "dashboard.status":
		if !hasID || hasProtocol || hasHash || hasURL || hasDelete || hasRef {
			return Request{}, errInvalid
		}
	case "torrent.pause", "torrent.resume":
		if !hasID || hasProtocol || !hasHash || !hasRef || hasURL || hasDelete {
			return Request{}, errInvalid
		}
	case "torrent.add":
		if !hasID || hasProtocol || !hasURL || !hasRef || hasHash || hasDelete {
			return Request{}, errInvalid
		}
	case "torrent.remove":
		if !hasID || hasProtocol || !hasHash || !hasRef || !hasDelete || hasURL {
			return Request{}, errInvalid
		}
	default:
		if hasProtocol || hasID || hasHash || hasURL || hasDelete || hasRef {
			return Request{}, errInvalid // unknown types are type-only
		}
	}
	return req, nil
}
