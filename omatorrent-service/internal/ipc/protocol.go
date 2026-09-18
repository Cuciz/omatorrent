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
	"strconv"
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
	Type     string
	Protocol int64
	ID       int64
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

func EncodeSnapshotItem(id int64, index int, t TorrentItem) []byte {
	t.Name = CapName(t.Name)
	return mustMarshal(snapshotItemResponse{"torrent.snapshot.item", ProtocolVersion, id, index, t})
}

func EncodeSnapshotEnd(id int64) []byte {
	return mustMarshal(snapshotEndResponse{"torrent.snapshot.end", ProtocolVersion, id})
}

// EncodeDeltas splits one change event into as many delta frames as the
// byte budget requires (all sharing seq; bounded per frame).
func EncodeDeltas(ev DeltaEvent) [][]byte {
	const budget = 3800 // headroom under MaxFrame for JSON overhead
	var out [][]byte
	var changed []TorrentItem
	var removed []string
	flush := func() {
		if len(changed) == 0 && len(removed) == 0 {
			changed, removed = nil, nil
			return
		}
		out = append(out, mustMarshal(deltaResponse{"torrent.delta", ProtocolVersion, ev.Seq, changed, removed}))
		changed, removed = nil, nil
	}
	size := 0
	for _, t := range ev.Changed {
		t.Name = CapName(t.Name)
		b, _ := json.Marshal(t)
		if size+len(b) > budget && (len(changed) > 0 || len(removed) > 0) {
			flush()
			size = 0
		}
		changed = append(changed, t)
		size += len(b)
	}
	for _, h := range ev.Removed {
		if size+len(h)+16 > budget && (len(changed) > 0 || len(removed) > 0) {
			flush()
			size = 0
		}
		removed = append(removed, h)
		size += len(h) + 16
	}
	flush()
	return out
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

		switch key {
		case "type":
			if len(raw) == 0 || raw[0] != '"' {
				return Request{}, errInvalid
			}
			var s string
			if json.Unmarshal(raw, &s) != nil {
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

	// Per-type exact key sets.
	switch req.Type {
	case "hello":
		if !hasProtocol || hasID {
			return Request{}, errInvalid
		}
	case "health", "system.status", "torrent.subscribe":
		if !hasID || hasProtocol {
			return Request{}, errInvalid
		}
	default:
		if hasProtocol || hasID {
			return Request{}, errInvalid // unknown types are type-only
		}
	}
	return req, nil
}
