package qbittorrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// mutTS records the mutation calls a fake backend receives.
type mutTS struct {
	mu    sync.Mutex
	calls []string // "METHOD path?form"
	ts    *httptest.Server
}

func newMutTS(t *testing.T, status int, body string) *mutTS {
	t.Helper()
	m := &mutTS{}
	m.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		m.mu.Lock()
		m.calls = append(m.calls, r.Method+" "+r.URL.Path+"?"+r.PostForm.Encode())
		m.mu.Unlock()
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(m.ts.Close)
	return m
}

func (m *mutTS) recorded() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.calls))
	copy(out, m.calls)
	return out
}

func TestStopStartSubmitOneHash(t *testing.T) {
	m := newMutTS(t, 200, "")
	c, _ := New(m.ts.URL, "", "")
	h := "0123456789abcdef0123456789abcdef01234567"
	if err := c.StopTorrent(context.Background(), h); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.StartTorrent(context.Background(), h); err != nil {
		t.Fatalf("start: %v", err)
	}
	got := m.recorded()
	want := []string{
		"POST /api/v2/torrents/stop?hashes=" + h,
		"POST /api/v2/torrents/start?hashes=" + h,
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call %d = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestPauseResumeLegacyPaths(t *testing.T) {
	m := newMutTS(t, 200, "")
	c, _ := New(m.ts.URL, "", "")
	h := "0123456789abcdef0123456789abcdef01234567"
	if err := c.PauseTorrent(context.Background(), h); err != nil {
		t.Fatalf("pause: %v", err)
	}
	if err := c.ResumeTorrent(context.Background(), h); err != nil {
		t.Fatalf("resume: %v", err)
	}
	got := m.recorded()
	if !strings.Contains(got[0], "/torrents/pause?hashes=") || !strings.Contains(got[1], "/torrents/resume?hashes=") {
		t.Fatalf("legacy paths wrong: %v", got)
	}
}

// deleteFiles is ALWAYS explicit — the daemon never relies on a backend
// default for the destructive distinction (ADR-0006).
func TestDeleteSendsExplicitDeleteFiles(t *testing.T) {
	m := newMutTS(t, 200, "")
	c, _ := New(m.ts.URL, "", "")
	h := "0123456789abcdef0123456789abcdef01234567"
	if err := c.DeleteTorrent(context.Background(), h, false); err != nil {
		t.Fatalf("delete false: %v", err)
	}
	if err := c.DeleteTorrent(context.Background(), h, true); err != nil {
		t.Fatalf("delete true: %v", err)
	}
	got := m.recorded()
	if got[0] != "POST /api/v2/torrents/delete?deleteFiles=false&hashes="+h {
		t.Fatalf("delete(false) = %q", got[0])
	}
	if got[1] != "POST /api/v2/torrents/delete?deleteFiles=true&hashes="+h {
		t.Fatalf("delete(true) = %q", got[1])
	}
}

func TestAddMagnetModernEcho(t *testing.T) {
	h := "0123456789abcdef0123456789abcdef01234567"
	m := newMutTS(t, 200, `{"added_torrent_ids":["`+h+`"],"failure_count":0,"pending_count":0,"success_count":1}`)
	c, _ := New(m.ts.URL, "", "")
	echo, err := c.AddMagnet(context.Background(), "magnet:?xt=urn:btih:"+h+"&dn=x")
	if err != nil {
		t.Fatalf("add: %v", err)
	}
	if len(echo) != 1 || echo[0] != h {
		t.Fatalf("echo = %v", echo)
	}
	got := m.recorded()[0]
	if !strings.HasPrefix(got, "POST /api/v2/torrents/add?") || !strings.Contains(got, "urls=magnet%3A%3Fxt%3Durn%3Abtih%3A") {
		t.Fatalf("add call = %q", got)
	}
}

func TestAddMagnetLegacyBody(t *testing.T) {
	m := newMutTS(t, 200, "Ok.")
	c, _ := New(m.ts.URL, "", "")
	echo, err := c.AddMagnet(context.Background(), "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567")
	if err != nil || echo != nil {
		t.Fatalf("legacy add: echo=%v err=%v", echo, err)
	}
}

// 409 = duplicate/malformed magnet on WebAPI ≥ 5.2-era backends
// (live-verified, docs/QBITTORRENT.md).
func TestAddMagnetConflict(t *testing.T) {
	m := newMutTS(t, http.StatusConflict, "Conflict")
	c, _ := New(m.ts.URL, "", "")
	_, err := c.AddMagnet(context.Background(), "magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567")
	if err != ErrConflict {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestMutationUnexpectedStatus(t *testing.T) {
	m := newMutTS(t, http.StatusNotFound, "Endpoint does not exist")
	c, _ := New(m.ts.URL, "", "")
	err := c.StopTorrent(context.Background(), "0123456789abcdef0123456789abcdef01234567")
	if err == nil || !strings.Contains(err.Error(), "unexpected response") {
		t.Fatalf("err = %v, want unexpected response", err)
	}
}

func TestMutationUnreachable(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close() // port now dead
	c, _ := New(url, "", "")
	err := c.DeleteTorrent(context.Background(), "0123456789abcdef0123456789abcdef01234567", false)
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v, want unreachable", err)
	}
}

// A 403 with credentials triggers exactly one re-login and one retry —
// mutations mirror the read path's SID-expiry behavior.
func TestMutationReloginRetry(t *testing.T) {
	var mu sync.Mutex
	loggedIn := 0
	stops := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			mu.Lock()
			loggedIn++
			mu.Unlock()
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "fresh-sid", Path: "/"})
			fmt.Fprint(w, "Ok.")
		case "/api/v2/torrents/stop":
			if len(r.Cookies()) == 0 {
				w.WriteHeader(http.StatusForbidden) // expired SID
				return
			}
			mu.Lock()
			stops++
			mu.Unlock()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	// No prior Login: the first stop attempt carries no SID, is refused
	// 403, performs one re-login and retries successfully.
	c, _ := New(ts.URL, "admin", "hunter2")
	if err := c.StopTorrent(context.Background(), "0123456789abcdef0123456789abcdef01234567"); err != nil {
		t.Fatalf("stop after relogin: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if loggedIn != 1 || stops != 1 {
		t.Fatalf("logins=%d stops=%d, want 1/1", loggedIn, stops)
	}
}
