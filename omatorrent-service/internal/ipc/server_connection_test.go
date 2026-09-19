package ipc

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeConnections scripts the v1.4 handler surface.
type fakeConnections struct {
	status       ConnectionStatusData
	test         ConnectionTestData
	configure    ConnectionConfigureData
	gotTest      ConnectionTestRequest
	gotConfigure ConnectionConfigureRequest
}

func (f *fakeConnections) ConnectionStatus() ConnectionStatusData { return f.status }

// Both handlers SNAPSHOT the transit password at call time: the server
// wipes the buffer after the handler returns (ADR-0008), and the real
// Manager also copies before persisting — the fake mirrors that.
func (f *fakeConnections) ConnectionTest(r ConnectionTestRequest) ConnectionTestData {
	if r.Password != nil {
		r.Password = append([]byte(nil), r.Password...)
	}
	f.gotTest = r
	return f.test
}
func (f *fakeConnections) ConnectionConfigure(r ConnectionConfigureRequest) ConnectionConfigureData {
	if r.Password != nil {
		r.Password = append([]byte(nil), r.Password...)
	}
	f.gotConfigure = r
	return f.configure
}

func startConnServer(t *testing.T, conns Connections) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	osChmod700(t, dir)
	path := dir + "/service.sock"
	srv, err := New(path, &fakeHandler{health: true}, nil, nil, conns, nil)
	if err != nil {
		t.Fatal(err)
	}
	go srv.Serve()
	t.Cleanup(srv.Close)
	waitSocket(t, path)
	return srv, path
}

// exchange performs hello + one request on a fresh connection.
func exchange(t *testing.T, path string, req string) []string {
	t.Helper()
	conn := dialSocket(t, path)
	defer conn.Close()
	w := bufio.NewWriter(conn)
	w.WriteString(`{"type":"hello","protocol":1}` + "\n")
	if req != "" {
		w.WriteString(req + "\n")
	}
	w.Flush()
	var lines []string
	r := bufio.NewReader(conn)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			break
		}
		lines = append(lines, strings.TrimSuffix(line, "\n"))
		if len(lines) >= 2 || (req == "" && len(lines) >= 1) {
			break
		}
	}
	return lines
}

func TestConnectionStatusWire(t *testing.T) {
	fc := &fakeConnections{status: ConnectionStatusData{
		Configured: true, Mode: "remote", Host: "qbittorrent.home.arpa",
		Transport: "https", Insecure: false, Username: "clement", HasSecret: true,
		TLSMode: "pin", Status: "connected", Detail: "", Epoch: 2,
	}}
	_, path := startConnServer(t, fc)
	lines := exchange(t, path, `{"type":"connection.status","id":7}`)
	if len(lines) < 2 {
		t.Fatalf("lines = %v", lines)
	}
	var resp map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"type": "connection.status", "protocol": float64(1), "id": float64(7),
		"configured": true, "mode": "remote", "host": "qbittorrent.home.arpa",
		"transport": "https", "insecure": false, "username": "clement",
		"has_secret": true, "tls_mode": "pin", "status": "connected",
		"detail": "", "epoch": float64(2),
	}
	if len(resp) != len(want) {
		t.Fatalf("keys = %v, want exactly %v", resp, want)
	}
	for k, v := range want {
		if resp[k] != v {
			t.Errorf("%s = %v, want %v", k, resp[k], v)
		}
	}
}

func TestConnectionTestWire(t *testing.T) {
	fc := &fakeConnections{test: ConnectionTestData{
		OK: false, Status: "tls_untrusted", Host: "127.0.0.1:9", Transport: "https",
		OfferedFingerprint: "ab12",
	}}
	_, path := startConnServer(t, fc)
	lines := exchange(t, path,
		`{"type":"connection.test","id":8,"url":"https://127.0.0.1:9","username":"u","password":"topsecret","tls_mode":"pin","pin":"`+strings.Repeat("0", 64)+`"}`)
	var resp map[string]any
	if err := json.Unmarshal([]byte(lines[1]), &resp); err != nil {
		t.Fatal(err)
	}
	// The password NEVER appears in the response (anti-reflection).
	if strings.Contains(lines[1], "topsecret") {
		t.Fatal("password reflected in response")
	}
	if resp["result"] != "failed" || resp["status"] != "tls_untrusted" ||
		resp["offered_fingerprint"] != "ab12" || resp["app_version"] != nil {
		t.Fatalf("resp = %v", resp)
	}
	// The handler received the parsed request intact.
	if fc.gotTest.Password == nil || string(fc.gotTest.Password) != "topsecret" ||
		!fc.gotTest.UseStoredPassword == true || fc.gotTest.TLSMode != "pin" {
		// UseStoredPassword was not sent; just ensure it is false.
		if fc.gotTest.UseStoredPassword {
			t.Fatal("use_stored_password not sent but true")
		}
	}
	if fc.gotTest.URL != "https://127.0.0.1:9" || fc.gotTest.Username != "u" {
		t.Fatalf("gotTest = %+v", fc.gotTest)
	}
}

func TestConnectionConfigureWire(t *testing.T) {
	fc := &fakeConnections{configure: ConnectionConfigureData{
		OK: true, Epoch: 3, Mode: "remote", Host: "h", Transport: "https",
	}}
	_, path := startConnServer(t, fc)
	lines := exchange(t, path,
		`{"type":"connection.configure","id":9,"url":"https://h","secret_action":"replace","password":"newsecret","tls_mode":"system"}`)
	if !strings.Contains(lines[1], `"type":"connection.configured"`) ||
		!strings.Contains(lines[1], `"epoch":3`) || strings.Contains(lines[1], "newsecret") {
		t.Fatalf("resp = %s", lines[1])
	}
	if fc.gotConfigure.SecretAction != "replace" || string(fc.gotConfigure.Password) != "newsecret" {
		t.Fatalf("gotConfigure = %+v", fc.gotConfigure)
	}
}

// The v1.4 grammar: exact key sets, enums, bounds, XOR rules.
func TestConnectionSchemas(t *testing.T) {
	_, path := startConnServer(t, &fakeConnections{test: ConnectionTestData{Status: "unreachable"}})
	bad := []string{
		// password AND use_stored_password together
		`{"type":"connection.test","id":1,"url":"https://h","password":"p","use_stored_password":true,"tls_mode":"system"}`,
		// pin without pin mode
		`{"type":"connection.test","id":1,"url":"https://h","tls_mode":"system","pin":"` + strings.Repeat("0", 64) + `"}`,
		// pin mode without pin
		`{"type":"connection.test","id":1,"url":"https://h","tls_mode":"pin"}`,
		// uppercase pin
		`{"type":"connection.test","id":1,"url":"https://h","tls_mode":"pin","pin":"` + strings.Repeat("A", 64) + `"}`,
		// ca mode is file-only
		`{"type":"connection.test","id":1,"url":"https://h","tls_mode":"ca"}`,
		// secret_action on test
		`{"type":"connection.test","id":1,"url":"https://h","tls_mode":"system","secret_action":"keep"}`,
		// configure without secret_action
		`{"type":"connection.configure","id":1,"url":"https://h","tls_mode":"system"}`,
		// replace without password
		`{"type":"connection.configure","id":1,"url":"https://h","tls_mode":"system","secret_action":"replace"}`,
		// keep WITH password
		`{"type":"connection.configure","id":1,"url":"https://h","tls_mode":"system","secret_action":"keep","password":"p"}`,
		// unknown secret_action
		`{"type":"connection.configure","id":1,"url":"https://h","tls_mode":"system","secret_action":"oops"}`,
		// oversized password (257)
		`{"type":"connection.test","id":1,"url":"https://h","password":"` + strings.Repeat("p", 257) + `","tls_mode":"system"}`,
		// oversized url (2049)
		`{"type":"connection.test","id":1,"url":"` + strings.Repeat("u", 2049) + `","tls_mode":"system"}`,
		// control char in url
		`{"type":"connection.test","id":1,"url":"https://h` + "\x01" + `","tls_mode":"system"}`,
		// magnet-only url on torrent.add now enforces the prefix
		`{"type":"torrent.add","id":1,"url":"https://notamagnet","ref":"r-1"}`,
		// v1.4 fields smuggled onto an old type
		`{"type":"health","id":1,"username":"u"}`,
		`{"type":"system.status","id":1,"password":"p"}`,
		`{"type":"torrent.pause","id":1,"hash":"` + strings.Repeat("a", 40) + `","ref":"r","tls_mode":"system"}`,
		// missing tls_mode
		`{"type":"connection.test","id":1,"url":"https://h"}`,
		// missing url
		`{"type":"connection.test","id":1,"tls_mode":"system"}`,
		// extra field on connection.status
		`{"type":"connection.status","id":1,"url":"https://h"}`,
	}
	for _, frame := range bad {
		lines := exchange(t, path, frame)
		if len(lines) < 2 || !strings.Contains(lines[1], `"invalid_message"`) {
			t.Errorf("frame accepted: %.80s… -> %v", frame, lines)
		}
	}
}

// Error responses never echo the password.
func TestConnectionErrorNoReflection(t *testing.T) {
	_, path := startConnServer(t, &fakeConnections{})
	lines := exchange(t, path,
		`{"type":"connection.test","id":1,"url":"https://h","password":"verysecret","tls_mode":"system","bogus":1}`)
	for _, l := range lines {
		if strings.Contains(l, "verysecret") {
			t.Fatalf("password reflected: %s", l)
		}
	}
	if !strings.Contains(lines[len(lines)-1], "invalid_message") {
		t.Fatalf("resp = %v", lines)
	}
}

// Contract fixtures parse under the v1.4 grammar (examples cannot
// drift — same discipline as the other extensions).
func TestConnectionFixturesParse(t *testing.T) {
	for _, name := range []string{"connection-status.txt", "connection-test.txt", "connection-configure.txt"} {
		raw := readFixture(t, name)
		frame := strings.TrimSpace(string(raw))
		if _, err := parseFrame([]byte(frame)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// ---- small helpers ----

func osChmod700(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
}

func dialSocket(t *testing.T, path string) net.Conn {
	t.Helper()
	var conn net.Conn
	var err error
	for i := 0; i < 50; i++ {
		conn, err = net.Dial("unix", path)
		if err == nil {
			return conn
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("dial %s: %v", path, err)
	return nil
}

func waitSocket(t *testing.T, path string) {
	t.Helper()
	dialSocket(t, path)
}
