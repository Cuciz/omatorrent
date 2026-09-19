package ipc

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- v1.2 mutation contract (ADR-0006) ----

const mutHash = "0123456789abcdef0123456789abcdef01234567"

type fakeMuts struct {
	mu        sync.Mutex
	submitted []MutationRequest
	answer    MutationStage1
	results   chan MutationResult
}

func (f *fakeMuts) Submit(r MutationRequest) MutationStage1 {
	f.mu.Lock()
	f.submitted = append(f.submitted, r)
	f.mu.Unlock()
	return f.answer
}

func (f *fakeMuts) calls() []MutationRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]MutationRequest, len(f.submitted))
	copy(out, f.submitted)
	return out
}

func (f *fakeMuts) Results() (<-chan MutationResult, func()) { return f.results, func() {} }

func startMutServer(t *testing.T, h Handler, muts Mutations) (*fakeMuts, string) {
	t.Helper()
	if muts == nil {
		path := startServerRaw(t, h, nil, nil)
		return nil, path
	}
	f, ok := muts.(*fakeMuts)
	if !ok {
		t.Fatal("startMutServer expects *fakeMuts")
	}
	return f, startServerRaw(t, h, nil, muts)
}

// startServerRaw mirrors startServer with explicit subs/muts.
func startServerRaw(t *testing.T, h Handler, subs Subscriptions, muts Mutations) string {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "service.sock")
	srv, err := New(path, h, subs, muts, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() {
		if err := srv.Serve(); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	t.Cleanup(srv.Close)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket did not appear")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return path
}

func newFakeMuts() *fakeMuts {
	return &fakeMuts{
		answer:  MutationStage1{Outcome: "accepted", Mutation: 12, Action: "torrent.pause", Hash: mutHash},
		results: make(chan MutationResult, 4),
	}
}

func TestMutationAcceptedFrameShape(t *testing.T) {
	f, path := startMutServer(t, &fakeHandler{}, newFakeMuts())
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"torrent.pause","id":4,"hash":"` + mutHash + `","ref":"panel-1"}`)
	want := `{"type":"mutation.accepted","protocol":1,"id":4,"mutation":12,"action":"torrent.pause","hash":"` + mutHash + `"}`
	if got := c.recv(); got != want {
		t.Fatalf("accepted frame = %s\nwant %s", got, want)
	}
	if len(f.calls()) != 1 {
		t.Fatalf("submit calls = %d", len(f.calls()))
	}
	// The connection stays usable after a mutation.
	c.send(`{"type":"health","id":5}`)
	if got := c.recv(); !strings.HasPrefix(got, `{"type":"health","protocol":1,"id":5`) {
		t.Fatalf("health after mutation = %s", got)
	}
}

func TestMutationRejectedKeepsConnection(t *testing.T) {
	f := newFakeMuts()
	f.answer = MutationStage1{Outcome: "stale_torrent"}
	_, path := startMutServer(t, &fakeHandler{health: true}, f)
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"torrent.pause","id":4,"hash":"` + mutHash + `","ref":"panel-1"}`)
	want := `{"type":"mutation.rejected","protocol":1,"id":4,"code":"stale_torrent"}`
	if got := c.recv(); got != want {
		t.Fatalf("rejected frame = %s\nwant %s", got, want)
	}
	c.send(`{"type":"health","id":5}`)
	if got := c.recv(); !strings.HasPrefix(got, `{"type":"health","protocol":1,"id":5`) {
		t.Fatalf("connection closed after rejection: %s", got)
	}
}

// delete_files is a REQUIRED boolean: ambiguity is a protocol violation
// and closes the connection (ADR-0006 destructive-safety rule).
func TestRemoveRequiresExplicitBooleanDeleteFiles(t *testing.T) {
	f := newFakeMuts()
	_, path := startMutServer(t, &fakeHandler{}, f)
	for _, frame := range []string{
		`{"type":"torrent.remove","id":4,"hash":"` + mutHash + `","ref":"r1"}`,
		`{"type":"torrent.remove","id":4,"hash":"` + mutHash + `","delete_files":"true","ref":"r1"}`,
		`{"type":"torrent.remove","id":4,"hash":"` + mutHash + `","delete_files":"false","ref":"r1"}`,
		`{"type":"torrent.remove","id":4,"hash":"` + mutHash + `","delete_files":1,"ref":"r1"}`,
		`{"type":"torrent.remove","id":4,"hash":"` + mutHash + `","delete_files":null,"ref":"r1"}`,
	} {
		c := dial(t, path)
		c.handshake()
		c.send(frame)
		if got := c.recv(); got != `{"type":"error","protocol":1,"code":"invalid_message"}` {
			t.Fatalf("frame %s: got %s, want invalid_message", frame, got)
		}
		// Connection must close afterward.
		c.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		if _, err := c.r.ReadString('\n'); err == nil {
			t.Fatalf("frame %s: connection not closed", frame)
		}
	}
	if len(f.calls()) != 0 {
		t.Fatalf("backend mutations submitted: %+v", f.calls())
	}
}

func TestRemoveForwardsExplicitIntent(t *testing.T) {
	f := newFakeMuts()
	_, path := startMutServer(t, &fakeHandler{}, f)
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"torrent.remove","id":4,"hash":"` + mutHash + `","delete_files":true,"ref":"r1"}`)
	c.recv() // accepted frame
	calls := f.calls()
	if len(calls) != 1 || !calls[0].DeleteFiles || calls[0].Action != "torrent.remove" || calls[0].Ref != "r1" {
		t.Fatalf("submitted = %+v (delete_files intent must pass through)", calls)
	}
}

func TestMutationRequestSchema(t *testing.T) {
	f := newFakeMuts()
	_, path := startMutServer(t, &fakeHandler{}, f)
	bad := []string{
		// hash must be 40/64 hex
		`{"type":"torrent.pause","id":4,"hash":"abc","ref":"r1"}`,
		// ref charset
		`{"type":"torrent.pause","id":4,"hash":"` + mutHash + `","ref":"has space"}`,
		`{"type":"torrent.pause","id":4,"hash":"` + mutHash + `","ref":""}`,
		`{"type":"torrent.pause","id":4,"hash":"` + mutHash + `","ref":"` + strings.Repeat("x", 129) + `"}`,
		// add url must be a magnet
		`{"type":"torrent.add","id":4,"url":"http://example.com/x.torrent","ref":"r1"}`,
		`{"type":"torrent.add","id":4,"url":"","ref":"r1"}`,
		`{"type":"torrent.add","id":4,"ref":"r1"}`,
		// url length cap (fits the 4096 frame, exceeds the field cap)
		`{"type":"torrent.add","id":4,"url":"magnet:?xt=urn:btih:` + strings.Repeat("0", 2048) + `","ref":"r1"}`,
		// exact key sets: no cross-action fields, no protocol
		`{"type":"torrent.pause","id":4,"hash":"` + mutHash + `","ref":"r1","protocol":1}`,
		`{"type":"torrent.pause","id":4,"hash":"` + mutHash + `","url":"magnet:?xt=urn:btih:` + mutHash + `","ref":"r1"}`,
		`{"type":"torrent.remove","id":4,"hash":"` + mutHash + `","delete_files":false}`,
		`{"type":"torrent.resume","id":4,"hash":"` + mutHash + `","delete_files":false,"ref":"r1"}`,
		// unknown types stay type-only
		`{"type":"torrent.mutate","id":4}`,
	}
	for _, frame := range bad {
		c := dial(t, path)
		c.handshake()
		c.send(frame)
		got := c.recv()
		want := `{"type":"error","protocol":1,"code":"invalid_message"}`
		if got != want {
			t.Fatalf("frame %.60s…: got %s, want %s", frame, got, want)
		}
	}
	if len(f.calls()) != 0 {
		t.Fatalf("unexpected submissions: %+v", f.calls())
	}
}

func TestMutationReplayReturnsResultWithID(t *testing.T) {
	f := newFakeMuts()
	f.answer = MutationStage1{
		Outcome: "accepted", Mutation: 9, Action: "torrent.remove", Hash: mutHash,
		Replay: &MutationResult{Mutation: 9, Action: "torrent.remove", Hash: mutHash, Status: "confirmed"},
	}
	_, path := startMutServer(t, &fakeHandler{}, f)
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"torrent.remove","id":77,"hash":"` + mutHash + `","delete_files":true,"ref":"r-old"}`)
	want := `{"type":"mutation.result","protocol":1,"id":77,"mutation":9,"action":"torrent.remove","hash":"` + mutHash + `","status":"confirmed"}`
	if got := c.recv(); got != want {
		t.Fatalf("replay frame = %s\nwant %s", got, want)
	}
}

// Result pushes reach only connections that issued a mutation request;
// a pure status client never sees them (bar widget unchanged).
func TestResultPushScope(t *testing.T) {
	f := newFakeMuts()
	_, path := startMutServer(t, &fakeHandler{health: true}, f)

	a := dial(t, path)
	a.handshake()
	a.send(`{"type":"torrent.pause","id":4,"hash":"` + mutHash + `","ref":"r1"}`)
	a.recv() // accepted

	b := dial(t, path)
	b.handshake()
	b.send(`{"type":"health","id":1}`)
	b.recv()

	f.results <- MutationResult{Mutation: 12, Action: "torrent.pause", Hash: mutHash, Status: "confirmed"}

	want := `{"type":"mutation.result","protocol":1,"mutation":12,"action":"torrent.pause","hash":"` + mutHash + `","status":"confirmed"}`
	if got := a.recv(); got != want {
		t.Fatalf("push to mutating conn = %s\nwant %s", got, want)
	}
	// b must NOT receive the push; its next frame is whatever it asks for.
	b.send(`{"type":"health","id":2}`)
	if got := b.recv(); !strings.HasPrefix(got, `{"type":"health","protocol":1,"id":2`) {
		t.Fatalf("status client received mutation push or garbage: %s", got)
	}
}

func TestMutationsDisabledServer(t *testing.T) {
	_, path := startMutServer(t, &fakeHandler{}, nil)
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"torrent.pause","id":4,"hash":"` + mutHash + `","ref":"r1"}`)
	if got := errCode(t, c.recv()); got != "unsupported_message" {
		t.Fatalf("code = %s", got)
	}
}

// v1.2 contract example files match the encoders and grammar
// (anti-drift, mirrors the v1.0/v1.1 example tests).
func TestContractExamplesV12(t *testing.T) {
	dir := filepath.Join(filepath.Dir(mustCallerFile(t)), "..", "..", "..", "contracts", "ipc", "v1")
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSuffix(string(b), "\n")
	}

	pause := read("torrent-pause.txt")
	if req, err := parseFrame([]byte(pause)); err != nil || req.Type != "torrent.pause" || req.ID != 4 || req.Hash != mutHash || req.Ref != "panel-1" {
		t.Fatalf("pause example invalid: %+v err=%v", req, err)
	}
	add := read("torrent-add.txt")
	if req, err := parseFrame([]byte(add)); err != nil || req.Type != "torrent.add" || !strings.HasPrefix(req.URL, "magnet:?xt=urn:btih:"+mutHash) || req.Ref != "panel-2" {
		t.Fatalf("add example invalid: %+v err=%v", req, err)
	}
	remove := read("torrent-remove.txt")
	if req, err := parseFrame([]byte(remove)); err != nil || req.Type != "torrent.remove" || req.DeleteFiles {
		t.Fatalf("remove example invalid: %+v err=%v", req, err)
	}
	if got := read("response-mutation-accepted.txt"); got != string(EncodeMutationAccepted(4, 12, "torrent.pause", mutHash)) {
		t.Fatalf("accepted example drifted: %s", got)
	}
	if got := read("response-mutation-rejected.txt"); got != string(EncodeMutationRejected(4, "stale_torrent")) {
		t.Fatalf("rejected example drifted: %s", got)
	}
	if got := read("mutation-result.txt"); got != string(EncodeMutationResult(MutationResult{Mutation: 12, Action: "torrent.pause", Hash: mutHash, Status: "confirmed"})) {
		t.Fatalf("result example drifted: %s", got)
	}
	if got := read("mutation-result-replay.txt"); got != string(EncodeMutationResultWithID(8, MutationResult{Mutation: 12, Action: "torrent.pause", Hash: mutHash, Status: "confirmed"})) {
		t.Fatalf("replay result example drifted: %s", got)
	}
}

// Pre-v1.2 request types must reject the v1.2 fields: exact key sets are
// part of the grammar (review finding — smuggled fields were parsed and
// silently ignored after v1.2 introduced them).
func TestPreV12TypesRejectMutationFields(t *testing.T) {
	_, path := startMutServer(t, &fakeHandler{health: true}, newFakeMuts())
	bad := []string{
		`{"type":"health","id":1,"ref":"x"}`,
		`{"type":"health","id":1,"hash":"` + mutHash + `"}`,
		`{"type":"system.status","id":1,"delete_files":true}`,
		`{"type":"system.status","id":1,"url":"magnet:?xt=urn:btih:` + mutHash + `"}`,
		`{"type":"torrent.subscribe","id":1,"ref":"x"}`,
		`{"type":"hello","protocol":1,"ref":"x"}`,
	}
	for _, frame := range bad {
		c := dial(t, path)
		c.handshake()
		c.send(frame)
		if got := c.recv(); got != `{"type":"error","protocol":1,"code":"invalid_message"}` {
			t.Fatalf("frame %s: got %s, want invalid_message", frame, got)
		}
	}
}
