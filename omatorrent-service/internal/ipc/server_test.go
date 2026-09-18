package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
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
	srv, err := New(path, h, nil)
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

	srv1, err := New(path, &fakeHandler{health: true}, nil)
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
	srv2, err := New(path, &fakeHandler{health: true}, nil)
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

	srv, err := New(path, &fakeHandler{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Serve(); err == nil || !strings.Contains(err.Error(), "refusing existing socket path") {
		t.Fatalf("expected refusal, got %v", err)
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
	srv, err := New(path, &fakeHandler{}, nil)
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
	srv, err := New(path, &fakeHandler{health: true}, nil)
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
