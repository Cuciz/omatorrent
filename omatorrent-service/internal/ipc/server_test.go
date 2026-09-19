package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// ---- test scaffolding ----

type fakeHandler struct {
	mu     sync.Mutex
	health bool
	status StatusData
}

func (f *fakeHandler) Health() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.health
}

func (f *fakeHandler) StatusData() (StatusData, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.health
}

func (f *fakeHandler) set(ok bool, d StatusData) {
	f.mu.Lock()
	f.health, f.status = ok, d
	f.mu.Unlock()
}

// startServer creates a private temp runtime dir (0700) and a server on
// an explicit socket path inside it, mirroring production lifecycle.
func startServer(t *testing.T, h Handler) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "service.sock")
	srv, err := New(path, h, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	go func() {
		if err := srv.Serve(); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	t.Cleanup(srv.Close)
	// Wait for the socket to appear.
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
	return srv, path
}

type client struct {
	conn net.Conn
	r    *bufio.Reader
	t    *testing.T
}

func dial(t *testing.T, path string) *client {
	t.Helper()
	conn, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return &client{conn: conn, r: bufio.NewReaderSize(conn, MaxFrame), t: t}
}

func (c *client) send(frame string) {
	c.t.Helper()
	c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.conn.Write([]byte(frame + "\n")); err != nil {
		c.t.Fatalf("send %s: %v", frame, err)
	}
}

func (c *client) sendRaw(b []byte) {
	c.t.Helper()
	c.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.conn.Write(b); err != nil {
		c.t.Fatalf("sendRaw: %v", err)
	}
}

func (c *client) recv() string {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	line, err := c.r.ReadString('\n')
	if err != nil {
		c.t.Fatalf("recv: %v", err)
	}
	return strings.TrimSuffix(line, "\n")
}

func (c *client) handshake() {
	c.t.Helper()
	c.send(`{"type":"hello","protocol":1}`)
	got := c.recv()
	want := `{"type":"hello","protocol":1,"service":"omatorrent-service"}`
	if got != want {
		c.t.Fatalf("hello response = %s, want %s", got, want)
	}
}

func errCode(t *testing.T, frame string) string {
	t.Helper()
	return strings.TrimSuffix(strings.TrimPrefix(frame, `{"type":"error","protocol":1,"code":"`), `"}`)
}

// contractFiles returns the example frames from contracts/ipc/v1.
func contractFiles(t *testing.T) map[string]string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "contracts", "ipc", "v1")
	out := map[string]string{}
	for _, name := range []string{"hello.txt", "health.txt", "system-status.txt", "response-hello.txt"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read contract example %s: %v", name, err)
		}
		out[name] = strings.TrimSuffix(string(b), "\n")
	}
	return out
}

// ---- contract tests ----

func TestContractExamplesMatchGrammar(t *testing.T) {
	ex := contractFiles(t)
	hello, err := parseFrame([]byte(strings.TrimSuffix(ex["hello.txt"], "")))
	if err != nil || hello.Type != "hello" || hello.Protocol != 1 {
		t.Fatalf("hello example invalid: %+v err=%v", hello, err)
	}
	h, err := parseFrame([]byte(ex["health.txt"]))
	if err != nil || h.Type != "health" || h.ID != 1 {
		t.Fatalf("health example invalid: %+v err=%v", h, err)
	}
	s, err := parseFrame([]byte(ex["system-status.txt"]))
	if err != nil || s.Type != "system.status" || s.ID != 2 {
		t.Fatalf("system.status example invalid: %+v err=%v", s, err)
	}
	if ex["response-hello.txt"] != string(EncodeHello()) {
		t.Fatalf("response-hello example drifted: %s vs %s", ex["response-hello.txt"], EncodeHello())
	}
}

func TestHandshakeAndHealth(t *testing.T) {
	h := &fakeHandler{health: true}
	_, path := startServer(t, h)
	ex := contractFiles(t)

	c := dial(t, path)
	c.send(ex["hello.txt"])
	if got := c.recv(); got != ex["response-hello.txt"] {
		t.Fatalf("hello = %s", got)
	}
	c.send(ex["health.txt"])
	got := c.recv()
	var resp struct {
		Type    string `json:"type"`
		Backend string `json:"backend"`
		Service string `json:"service"`
	}
	if err := json.Unmarshal([]byte(got), &resp); err != nil {
		t.Fatalf("health response: %v", err)
	}
	if resp.Type != "health" || resp.Service != "ready" || resp.Backend != "ok" {
		t.Fatalf("health response = %s", got)
	}

	// Health may be requested repeatedly.
	c.send(`{"type":"health","id":2}`)
	c.recv()
}

func TestSystemStatusOKAndDegraded(t *testing.T) {
	h := &fakeHandler{}
	_, path := startServer(t, h)
	c := dial(t, path)
	c.handshake()

	h.set(true, StatusData{AppVersion: "v5.2.3", WebAPIVersion: "2.15.1", DlSpeed: 1024, UpSpeed: 2048, TorrentsTotal: 3})
	c.send(`{"type":"system.status","id":5}`)
	want := `{"type":"system.status","protocol":1,"id":5,"qbittorrent":"ok","app_version":"v5.2.3","webapi_version":"2.15.1","dl_speed":1024,"up_speed":2048,"torrents_total":3}`
	if got := c.recv(); got != want {
		t.Fatalf("status ok = %s, want %s", got, want)
	}

	h.set(false, StatusData{})
	c.send(`{"type":"system.status","id":6}`)
	want = `{"type":"system.status","protocol":1,"id":6,"qbittorrent":"unavailable"}`
	if got := c.recv(); got != want {
		t.Fatalf("status degraded = %s, want %s", got, want)
	}
}

func TestVersionMismatch(t *testing.T) {
	_, path := startServer(t, &fakeHandler{})
	c := dial(t, path)
	c.send(`{"type":"hello","protocol":2}`)
	if got := errCode(t, c.recv()); got != "version_mismatch" {
		t.Fatalf("code = %s", got)
	}
	// Connection closes after error.
	if _, err := c.r.ReadString('\n'); err == nil {
		t.Fatal("connection not closed after error")
	}
}

func TestHandshakeRequired(t *testing.T) {
	_, path := startServer(t, &fakeHandler{})
	c := dial(t, path)
	c.send(`{"type":"health","id":1}`)
	if got := errCode(t, c.recv()); got != "handshake_required" {
		t.Fatalf("code = %s", got)
	}
}

func TestMalformedMessagesDoNotCrashService(t *testing.T) {
	h := &fakeHandler{health: true}
	_, path := startServer(t, h)

	cases := [][]byte{
		[]byte("not json\n"),
		[]byte("{\n"), // incomplete frame then close
		[]byte("{\"type\":\"hello\",\"type\":\"hello\",\"protocol\":1}\n"),
		[]byte("{\"type\":\"health\",\"id\":1.5}\n"),
		[]byte("{\"type\":\"health\",\"id\":\"x\"}\n"),
		[]byte("{\"type\":\"health\",\"id\":1,\"extra\":true}\n"),
		{0x7b, 0x22, 't', 'y', 'p', 'e', 0x22, 0x3a, 0x22, 'x', 0x22, 0xff, 0x7d, '\n'}, // invalid UTF-8
		[]byte("[1,2]\n"),
		[]byte("null\n"),
		[]byte("{\"type\":\"hello\",\"protocol\":1} {\"type\":\"x\"}\n"),
	}
	for i, frame := range cases {
		c := dial(t, path)
		c.sendRaw(frame)
		resp := c.recv()
		if !strings.HasPrefix(resp, `{"type":"error","protocol":1,"code":"invalid_message"}`) {
			t.Fatalf("case %d: response = %s", i, resp)
		}
	}

	// Service still healthy afterwards.
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"health","id":1}`)
	if got := c.recv(); !strings.Contains(got, `"backend":"ok"`) {
		t.Fatalf("post-malformed health = %s", got)
	}
}

func TestMessageTooLarge(t *testing.T) {
	_, path := startServer(t, &fakeHandler{})
	c := dial(t, path)

	// Exactly 4096 bytes including LF is valid: hello + spaces.
	exact := []byte(`{"type":"hello","protocol":1}`)
	for len(exact) < MaxFrame-1 {
		exact = append(exact, ' ')
	}
	exact = append(exact, '\n')
	if len(exact) != MaxFrame {
		t.Fatalf("exact frame built %d bytes", len(exact))
	}
	c.sendRaw(exact)
	if got := c.recv(); !strings.Contains(got, `"service":"omatorrent-service"`) {
		t.Fatalf("exact-4096 hello rejected: %s", got)
	}

	// 4097 bytes including LF is too large.
	c2 := dial(t, path)
	big := []byte(`{"type":"hello","protocol":1}`)
	for len(big) < MaxFrame {
		big = append(big, ' ')
	}
	big = append(big, '\n')
	c2.sendRaw(big)
	if got := errCode(t, c2.recv()); got != "message_too_large" {
		t.Fatalf("code = %s", got)
	}
}

func TestUnsupportedAfterHandshake(t *testing.T) {
	_, path := startServer(t, &fakeHandler{})
	c := dial(t, path)
	c.handshake()

	c.send(`{"type":"hello","protocol":1}`)
	if got := errCode(t, c.recv()); got != "unsupported_message" {
		t.Fatalf("second hello: code = %s", got)
	}

	c2 := dial(t, path)
	c2.handshake()
	c2.send(`{"type":"torrent.snapshot"}`)
	if got := errCode(t, c2.recv()); got != "unsupported_message" {
		t.Fatalf("future op: code = %s", got)
	}
}

func TestCleanDisconnectAndReconnect(t *testing.T) {
	_, path := startServer(t, &fakeHandler{health: true})

	// Disconnect without any frame.
	c1 := dial(t, path)
	c1.conn.Close()

	// Reconnect and full exchange.
	c2 := dial(t, path)
	c2.handshake()
	c2.send(`{"type":"health","id":1}`)
	c2.recv()
}

func TestReconnectAfterServerRestart(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "service.sock")

	srv1, err := New(path, &fakeHandler{health: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv1.Serve()
	waitForSocket(t, path)

	c := dial(t, path)
	c.handshake()
	srv1.Close()

	// Orderly shutdown removed the socket.
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket not removed: %v", err)
	}

	// Restart: a fresh server can bind and the client handshakes again.
	srv2, err := New(path, &fakeHandler{health: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv2.Serve()
	waitForSocket(t, path)
	t.Cleanup(srv2.Close)

	c2 := dial(t, path)
	c2.handshake()
	c2.send(`{"type":"health","id":1}`)
	c2.recv()
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("socket %s never appeared", path)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestStaleSocketRefused(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "service.sock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Close()

	srv, err := New(path, &fakeHandler{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Serve(); err == nil || !strings.Contains(err.Error(), "refusing non-socket file") {
		t.Fatalf("expected refusal, got %v", err)
	}
}

// makeStaleSocket creates a dead unix socket of exactly the shape the
// daemon would leave behind after an unclean exit (0600, owned, listener
// closed without unlink).
func makeStaleSocket(t *testing.T, path string) {
	t.Helper()
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	ln.Close()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

// A socket left behind by a SIGKILL'd daemon (listener dead) is removed
// and the new instance serves on the path.
func TestStaleSocketRecovered(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "service.sock")
	makeStaleSocket(t, path)

	srv, err := New(path, &fakeHandler{health: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := srv.Serve(); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	t.Cleanup(srv.Close)

	// The stale file exists from the start, so poll by dialing the NEW
	// listener rather than watching the path.
	c := waitDialable(t, path)
	c.handshake()
	c.send(`{"type":"health","id":1}`)
	c.recv()
}

// waitDialable retries dialing until the new listener answers (the path
// may exist as a stale file before recovery completes).
func waitDialable(t *testing.T, path string) *client {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.Dial("unix", path)
		if err == nil {
			t.Cleanup(func() { conn.Close() })
			return &client{conn: conn, r: bufio.NewReaderSize(conn, MaxFrame), t: t}
		}
		if time.Now().After(deadline) {
			t.Fatalf("listener on %s never answered: %v", path, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A live daemon answering IPC v1 on the socket forbids a second instance.
func TestActiveDaemonRefused(t *testing.T) {
	_, path := startServer(t, &fakeHandler{health: true}) // live daemon

	srv2, err := New(path, &fakeHandler{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv2.Serve(); err == nil || !strings.Contains(err.Error(), "another omatorrent-service is answering") {
		t.Fatalf("expected live-daemon refusal, got %v", err)
	}
}

// Stale socket with permissive permissions is never touched.
func TestStaleSocketWrongPermsRefused(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "service.sock")
	makeStaleSocket(t, path)
	os.Chmod(path, 0o666)

	srv, err := New(path, &fakeHandler{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Serve(); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("expected perms refusal, got %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("unsafe socket was removed: %v", err)
	}
}

// A symlink at the socket path is refused untouched.
func TestSocketSymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	real := filepath.Join(dir, "real.sock")
	makeStaleSocket(t, real)
	path := filepath.Join(dir, "service.sock")
	os.Symlink(real, path)

	srv, err := New(path, &fakeHandler{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Serve(); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink refusal, got %v", err)
	}
	if fi, err := os.Lstat(path); err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink was removed or replaced: %v", err)
	}
}

// Shutdown must leave a socket that was replaced under the listener in
// place (identity mismatch → not ours to delete).
func TestShutdownLeavesReplacedSocket(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "service.sock")
	srv, err := New(path, &fakeHandler{health: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	waitForSocket(t, path)

	// Swap the socket for a foreign file while the server runs.
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	foreign := []byte("not a socket")
	if err := os.WriteFile(path, foreign, 0o600); err != nil {
		t.Fatal(err)
	}

	srv.Close()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("replaced path was removed on shutdown: %v", err)
	}
	if string(got) != string(foreign) {
		t.Fatalf("foreign file was altered on shutdown")
	}
}

// A listener that accepts connections but never answers the hello is
// ambiguous: startup fails closed, the socket is not removed.
func TestHangingListenerRefused(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "service.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	os.Chmod(path, 0o600)
	go func() { // accept and hold connections silently
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close()
		}
	}()

	srv, err := New(path, &fakeHandler{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Serve(); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguity refusal, got %v", err)
	}
	if _, err := os.Lstat(path); err != nil {
		t.Fatalf("ambiguous socket was removed: %v", err)
	}
}

func TestClientLimit(t *testing.T) {
	_, path := startServer(t, &fakeHandler{health: true})
	var conns []*client
	for i := 0; i < maxClients; i++ {
		c := dial(t, path)
		c.handshake()
		conns = append(conns, c)
	}
	extra, err := net.Dial("unix", path)
	if err != nil {
		t.Fatalf("dial extra: %v", err)
	}
	defer extra.Close()
	extra.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	if n, err := extra.Read(buf); n > 0 || err == nil {
		t.Fatalf("excess client got response (%d bytes, err=%v)", n, err)
	}
	// Existing clients keep working.
	conns[0].send(`{"type":"health","id":1}`)
	conns[0].recv()
}

func TestSocketPermissions(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "service.sock")
	srv, err := New(path, &fakeHandler{}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	waitForSocket(t, path)
	t.Cleanup(srv.Close)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %04o, want 0600", fi.Mode().Perm())
	}
}

func TestResolveSocketPathRejectsUnsafeDirs(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o755)
	if _, err := ResolveSocketPath(filepath.Join(dir, "sock")); err == nil {
		t.Fatal("group/world-accessible parent accepted")
	}
	os.Chmod(dir, 0o700)
	if _, err := ResolveSocketPath(filepath.Join(dir, "sock")); err != nil {
		t.Fatalf("private parent rejected: %v", err)
	}
	if _, err := ResolveSocketPath("relative/sock"); err == nil {
		t.Fatal("relative path accepted")
	}
}

func TestShutdownClosesActiveClients(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "service.sock")
	srv, err := New(path, &fakeHandler{health: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	waitForSocket(t, path)

	c, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Half-sent frame: client stalls mid-frame.
	c.Write([]byte(`{"type":"hel`))
	srv.Close()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 16)
	if _, err := c.Read(buf); err == nil {
		t.Fatal("stalled client not closed on shutdown")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("socket not removed on shutdown")
	}
}

// daemonUnavailableThroughIPC proves the degraded path end-to-end shape.
func TestDaemonUnavailableRenderPath(t *testing.T) {
	h := &fakeHandler{}
	_, path := startServer(t, h)
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"health","id":1}`)
	if got := c.recv(); !strings.Contains(got, `"backend":"unavailable"`) {
		t.Fatalf("health = %s", got)
	}
	c.send(`{"type":"system.status","id":2}`)
	if got := c.recv(); !strings.Contains(got, `"qbittorrent":"unavailable"`) {
		t.Fatalf("status = %s", got)
	}
}

// ---- v1.1 subscription contract tests (ADR-0005) ----

type fakeSubs struct {
	mu     sync.Mutex
	items  []TorrentItem
	events chan DeltaEvent
}

func (f *fakeSubs) Subscribe() ([]TorrentItem, <-chan DeltaEvent, func()) {
	items := make([]TorrentItem, len(f.items))
	copy(items, f.items)
	cancel := func() {}
	return items, f.events, cancel
}

func startSubServer(t *testing.T, subs Subscriptions) (*fakeSubs, string) {
	t.Helper()
	fs := subs.(*fakeSubs)
	// Pre-create the events channel BEFORE the server starts so tests
	// can push from any goroutine without racing lazy initialization.
	if fs.events == nil {
		fs.events = make(chan DeltaEvent, 4096)
	}
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	path := filepath.Join(dir, "service.sock")
	srv, err := New(path, &fakeHandler{health: true}, fs, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := srv.Serve(); err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	waitForSocket(t, path)
	t.Cleanup(srv.Close)
	return fs, path
}

func (c *client) recvRaw() []string {
	c.t.Helper()
	var lines []string
	for {
		c.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		line, err := c.r.ReadString('\n')
		if err != nil {
			return lines
		}
		lines = append(lines, strings.TrimSuffix(line, "\n"))
	}
}

func TestSubscriptionSnapshotAndDelta(t *testing.T) {
	subs := &fakeSubs{items: []TorrentItem{
		{Hash: "aa", Name: "Zeta", State: "seeding"},
		{Hash: "bb", Name: "Alpha", State: "downloading", Progress: 0.5},
	}}
	_, path := startSubServer(t, subs)

	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"torrent.subscribe","id":7}`)
	c.send(`{"type":"torrent.subscribe","id":8}`) // second subscribe → unsupported
	lines := c.recvRaw()

	var sawSubscribed, sawBegin, sawEnd, sawSecond bool
	var items int
	for _, l := range lines {
		switch {
		case l == `{"type":"torrent.subscribed","protocol":1,"id":7}`:
			sawSubscribed = true
		case l == `{"type":"torrent.snapshot.begin","protocol":1,"id":7,"count":2}`:
			sawBegin = true
		case strings.HasPrefix(l, `{"type":"torrent.snapshot.item","protocol":1,"id":7,"index":`):
			items++
		case l == `{"type":"torrent.snapshot.end","protocol":1,"id":7}`:
			sawEnd = true
		case l == `{"type":"torrent.subscribed","protocol":1,"id":8}`:
			sawSecond = true
		}
	}
	if !sawSubscribed || !sawBegin || !sawEnd || items != 2 || sawSecond {
		t.Fatalf("subscription frames: subscribed=%v begin=%v end=%v items=%d secondAccepted=%v lines=%v",
			sawSubscribed, sawBegin, sawEnd, items, sawSecond, lines)
	}
	// Wire order is sorted by name: Alpha before Zeta (lines: subscribed,
	// begin, item0, item1, end).
	if !strings.Contains(lines[2], `"name":"Alpha"`) || !strings.Contains(lines[3], `"name":"Zeta"`) {
		t.Fatalf("snapshot not name-sorted: %v", lines)
	}

	// Push a delta; it must arrive as one frame.
	subs.events <- DeltaEvent{Seq: 5, Changed: []TorrentItem{{Hash: "bb", Name: "Alpha", State: "downloading", DlSpeed: 42}}, Removed: []string{"aa"}}
	lines = c.recvRaw()
	found := false
	for _, l := range lines {
		if strings.HasPrefix(l, `{"type":"torrent.delta","protocol":1,"seq":5,"changed":[`) &&
			strings.Contains(l, `"dlspeed":42`) && strings.Contains(l, `"removed":["aa"]`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("delta frame not received: %v", lines)
	}

	// v1.0 requests still work on the subscribed connection.
	c.send(`{"type":"health","id":9}`)
	lines = c.recvRaw()
	ok := false
	for _, l := range lines {
		if strings.Contains(l, `"backend":"ok"`) {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("health on subscribed connection failed: %v", lines)
	}
}

func TestSubscriptionInvalidSchema(t *testing.T) {
	_, path := startSubServer(t, &fakeSubs{})
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"torrent.subscribe"}`) // missing id
	if got := errCode(t, c.recv()); got != "invalid_message" {
		t.Fatalf("code = %s", got)
	}
}

func TestSubscriptionReconnectResubscribe(t *testing.T) {
	subs := &fakeSubs{items: []TorrentItem{{Hash: "aa", Name: "Only", State: "paused"}}}
	_, path := startSubServer(t, subs)

	c1 := dial(t, path)
	c1.handshake()
	c1.send(`{"type":"torrent.subscribe","id":1}`)
	lines := c1.recvRaw()
	if len(lines) != 4 { // subscribed + begin + 1 item + end
		t.Fatalf("first subscription frames = %v", lines)
	}
	c1.conn.Close()

	// Reconnect: fresh handshake + fresh full snapshot.
	c2 := dial(t, path)
	c2.handshake()
	c2.send(`{"type":"torrent.subscribe","id":2}`)
	lines = c2.recvRaw()
	if len(lines) != 4 || !strings.Contains(lines[2], `"name":"Only"`) {
		t.Fatalf("resubscription frames = %v", lines)
	}
}

// Big deltas are split into bounded frames sharing one seq.
func TestDeltaChunking(t *testing.T) {
	ev := DeltaEvent{Seq: 9}
	for i := 0; i < 60; i++ {
		name := strings.Repeat("n", 200) + string(rune('a'+i%26))
		ev.Changed = append(ev.Changed, TorrentItem{Hash: fmt.Sprintf("%040d", i), Name: name, State: "downloading"})
	}
	frames, ok := EncodeDeltas(ev)
	if !ok {
		t.Fatal("normal delta failed to encode")
	}
	if len(frames) < 2 {
		t.Fatalf("expected chunking, got %d frames", len(frames))
	}
	for i, f := range frames {
		if len(f)+1 > MaxFrame {
			t.Fatalf("frame %d exceeds budget: %d bytes", i, len(f)+1)
		}
		if !strings.HasPrefix(string(f), `{"type":"torrent.delta","protocol":1,"seq":9,`) {
			t.Fatalf("frame %d wrong prefix: %s", i, f[:60])
		}
	}
	total := 0
	for _, f := range frames {
		var d deltaResponse
		if err := json.Unmarshal(f, &d); err != nil {
			t.Fatalf("chunk decode: %v", err)
		}
		total += len(d.Changed)
	}
	if total != 60 {
		t.Fatalf("chunks carried %d torrents, want 60", total)
	}
}

// Name cap: wire items never exceed NameCapRunes runes.
func TestNameCap(t *testing.T) {
	long := strings.Repeat("x", 2000)
	item := TorrentItem{Hash: "aa", Name: long, State: "paused"}
	frame := EncodeSnapshotItem(1, 0, item)
	if len(frame)+1 > MaxFrame {
		t.Fatalf("capped item exceeds frame budget: %d", len(frame)+1)
	}
	var got snapshotItemResponse
	if err := json.Unmarshal(frame, &got); err != nil {
		t.Fatal(err)
	}
	if len([]rune(got.Torrent.Name)) != NameCapRunes {
		t.Fatalf("name runes = %d", len([]rune(got.Torrent.Name)))
	}
}

// Slow consumers are disconnected rather than growing daemon memory.
func TestSlowSubscriberDisconnected(t *testing.T) {
	subs := &fakeSubs{items: nil}
	_, path := startSubServer(t, subs)
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"torrent.subscribe","id":1}`)
	c.recvRaw() // drain snapshot

	// Do not read; push far more than the queue capacity.
	for i := 0; i < outQueue+50; i++ {
		select {
		case subs.events <- DeltaEvent{Seq: uint64(i), Changed: []TorrentItem{{Hash: "aa", Name: "n", State: "paused"}}}:
		default:
			// channel full: server may already be tearing down
		}
	}
	// The connection must eventually close.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		buf := make([]byte, 1<<16)
		if _, err := c.r.Read(buf); err != nil {
			return // closed
		}
	}
	t.Fatal("slow subscriber was not disconnected")
}

// v1.1 contract example files match the encoders (anti-drift).
func TestContractExamplesV11(t *testing.T) {
	dir := filepath.Join(filepath.Dir(mustCallerFile(t)), "..", "..", "..", "contracts", "ipc", "v1")
	sub, err := os.ReadFile(filepath.Join(dir, "torrent-subscribe.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSuffix(string(sub), "\n"); got != `{"type":"torrent.subscribe","id":3}` {
		t.Fatalf("subscribe example drifted: %s", got)
	}
	resp, err := os.ReadFile(filepath.Join(dir, "response-torrent-subscribed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSuffix(string(resp), "\n"); got != string(EncodeSubscribed(3)) {
		t.Fatalf("subscribed example drifted: %s vs %s", got, EncodeSubscribed(3))
	}
}

func mustCallerFile(t *testing.T) string {
	t.Helper()
	_, f, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	return f
}

// ---- review-round fixes: oversize handling + snapshot flow control ----

func manyItems(n int) []TorrentItem {
	items := make([]TorrentItem, n)
	for i := range items {
		items[i] = TorrentItem{
			Hash: fmt.Sprintf("%040x", i), Name: fmt.Sprintf("Torrent %04d", i),
			State: "downloading", Progress: float64(i%100) / 100, DlSpeed: int64(i),
			UpSpeed: int64(i * 2), Eta: int64(3600 + i), Ratio: float64(i) / 10,
			Category: "cat", Size: 1 << 20, Completed: 1 << 19,
		}
	}
	return items
}

// A committed item that cannot fit the delta budget is never silently
// dropped: EncodeDeltas refuses, and the server disconnects the
// subscriber so it rebuilds from a fresh snapshot.
func TestOversizeDeltaDisconnects(t *testing.T) {
	pathological := TorrentItem{
		Hash:     strings.Repeat("a", 64),
		Name:     string([]rune{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}), // heavy escaping
		Category: string([]rune{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}),
		State:    "other",
	}
	// Amplify past the budget with a max-length control-char name.
	pathological.Name = strings.Repeat("\u001f", 512)
	pathological.Category = strings.Repeat("\u001f", 128)
	b, _ := json.Marshal(pathological)
	if len(b) < 3800 {
		t.Fatalf("fixture not pathological enough: %d bytes", len(b))
	}
	if _, ok := EncodeDeltas(DeltaEvent{Seq: 1, Changed: []TorrentItem{pathological}}); ok {
		t.Fatal("oversize item unexpectedly encodable")
	}

	// Server-level: subscriber receives the oversize event → disconnect.
	subs := &fakeSubs{items: []TorrentItem{{Hash: "aa", Name: "n", State: "paused"}}}
	_, path := startSubServer(t, subs)
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"torrent.subscribe","id":1}`)
	c.recvRaw() // drain snapshot
	subs.events <- DeltaEvent{Seq: 2, Changed: []TorrentItem{pathological}}
	// Connection must close (no silent loss, no partial frames applied).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		buf := make([]byte, 1<<16)
		if _, err := c.r.Read(buf); err != nil {
			return
		}
	}
	t.Fatal("subscriber not disconnected after unframmable delta")
}

// Snapshots far larger than the 256-frame live queue must be delivered
// completely to a healthy reader (backpressure, not overflow).
func TestSnapshotLargeNoOverflow(t *testing.T) {
	for _, n := range []int{1, 256, 1000} {
		t.Run(fmt.Sprintf("n=%d", n), func(t *testing.T) {
			subs := &fakeSubs{items: manyItems(n)}
			_, path := startSubServer(t, subs)
			c := dial(t, path)
			c.handshake()
			c.send(`{"type":"torrent.subscribe","id":1}`)

			// Read with a modest deadline; the reader is fast (local pipe).
			var frames []string
			deadline := time.Now().Add(10 * time.Second)
			for len(frames) < n+3 && time.Now().Before(deadline) {
				c.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
				line, err := c.r.ReadString('\n')
				if err != nil {
					continue
				}
				frames = append(frames, strings.TrimSuffix(line, "\n"))
			}
			if len(frames) != n+3 {
				t.Fatalf("n=%d: received %d frames, want %d (subscribed+begin+%d items+end)", n, len(frames), n+3, n)
			}
			if !strings.Contains(frames[1], `"count":`) || !strings.Contains(frames[len(frames)-1], "snapshot.end") {
				t.Fatalf("n=%d: frame sequence wrong: %v ...", n, frames[:3])
			}
			for _, f := range frames {
				if len(f)+1 > MaxFrame {
					t.Fatalf("n=%d: frame exceeds budget: %d bytes", n, len(f)+1)
				}
			}
		})
	}
}

// A subscriber that never drains a large snapshot is disconnected after
// the bounded delivery window (slow-consumer rule still applies).
func TestSlowSnapshotSubscriberDisconnected(t *testing.T) {
	old := snapshotDeliveryTimeout
	snapshotDeliveryTimeout = 1500 * time.Millisecond
	t.Cleanup(func() { snapshotDeliveryTimeout = old })

	subs := &fakeSubs{items: manyItems(1000)}
	_, path := startSubServer(t, subs)
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"torrent.subscribe","id":1}`)
	// Do not read anything.

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		buf := make([]byte, 1<<16)
		if _, err := c.r.Read(buf); err != nil {
			return // closed
		}
	}
	t.Fatal("slow snapshot subscriber was not disconnected")
}

// A change committed while the snapshot is being delivered must arrive
// strictly AFTER snapshot.end (registration atomicity + ordering).
func TestDeltaDuringSnapshotOrdering(t *testing.T) {
	subs := &fakeSubs{items: manyItems(1000)}
	_, path := startSubServer(t, subs)
	c := dial(t, path)
	c.handshake()
	c.send(`{"type":"torrent.subscribe","id":1}`)
	// Wait until delivery is in progress (queue backpressure with a
	// non-reading client would stall it; so read slowly in chunks and
	// push the delta mid-way).
	go func() {
		time.Sleep(50 * time.Millisecond) // snapshot delivery underway
		subs.events <- DeltaEvent{Seq: 99, Changed: []TorrentItem{{Hash: "aa", Name: "changed", State: "paused"}}}
	}()

	var frames []string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		c.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		line, err := c.r.ReadString('\n')
		if err != nil {
			if len(frames) >= 1000+4 {
				break
			}
			continue
		}
		frames = append(frames, strings.TrimSuffix(line, "\n"))
		if strings.Contains(line, `"seq":99`) && len(frames) >= 1000+4 {
			break
		}
	}
	endIdx, deltaIdx := -1, -1
	for i, f := range frames {
		if strings.Contains(f, "snapshot.end") && endIdx < 0 {
			endIdx = i
		}
		if strings.Contains(f, `"seq":99`) {
			deltaIdx = i
		}
	}
	if endIdx < 0 || deltaIdx < 0 {
		t.Fatalf("missing frames: end=%d delta=%d total=%d", endIdx, deltaIdx, len(frames))
	}
	if deltaIdx < endIdx {
		t.Fatalf("delta (frame %d) arrived before snapshot.end (%d)", deltaIdx, endIdx)
	}
}
