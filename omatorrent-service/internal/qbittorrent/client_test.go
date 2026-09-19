package qbittorrent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLoginSuccess(t *testing.T) {
	var gotReferer string
	var gotUser, gotPass string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/auth/login" {
			gotReferer = r.Header.Get("Referer")
			gotUser, gotPass = r.PostFormValue("username"), r.PostFormValue("password")
			// Real qBittorrent scopes the session cookie to the whole
			// API (path=/, observed live).
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "live-sid", Path: "/"})
			fmt.Fprint(w, "Ok.")
			return
		}
		if r.URL.Path == "/api/v2/app/version" {
			if len(r.Cookies()) == 0 {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			fmt.Fprint(w, "v5.2.3")
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	c, err := New(ts.URL, "admin", "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.HasPrefix(gotReferer, "http://127.0.0.1:") {
		t.Fatalf("referer = %q", gotReferer)
	}
	if gotUser != "admin" || gotPass != "hunter2" {
		t.Fatal("credentials not posted")
	}
	v, err := c.AppVersion(context.Background())
	if err != nil || v != "v5.2.3" {
		t.Fatalf("AppVersion = %q err=%v", v, err)
	}
}

func TestLoginBadCredentials(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Fails.")
	}))
	defer ts.Close()

	c, _ := New(ts.URL, "admin", "wrong")
	err := c.Login(context.Background())
	if err == nil || !strings.Contains(err.Error(), "bad credentials") {
		t.Fatalf("err = %v, want bad credentials", err)
	}
}

func TestLoginBanned(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()

	c, _ := New(ts.URL, "admin", "x")
	err := c.Login(context.Background())
	if err != ErrBanned {
		t.Fatalf("err = %v, want ErrBanned", err)
	}
}

func TestUnreachable(t *testing.T) {
	// Closed port on localhost.
	c, _ := New("http://127.0.0.1:1", "", "")
	_, err := c.SyncMaindata(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v, want unreachable", err)
	}
}

// The localhost-bypass session must work without credentials: the
// server-issued cookie sticks in the jar and the rid advances.
func TestSyncMaindataBypassSessionAndDeltas(t *testing.T) {
	sids := 0
	serveMaindata := func(w http.ResponseWriter, r *http.Request) {
		sids++
		http.SetCookie(w, &http.Cookie{Name: "QBT_SID_8080", Value: fmt.Sprintf("sid-%d", sids), Path: "/"})
		switch {
		case r.URL.Query().Get("rid") == "0" || len(r.Cookies()) == 0:
			fmt.Fprint(w, `{"rid":1,"full_update":true,"torrents":{"aa":{"name":"A","state":"downloading","progress":0.5,"dlspeed":10,"upspeed":0,"eta":60,"ratio":0.1,"size":100,"completed":50}},"server_state":{"dl_info_speed":10,"up_info_speed":0,"connection_status":"connected"}}`)
		default:
			fmt.Fprint(w, `{"rid":2,"torrents":{},"torrents_removed":[]}`)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/sync/maindata" {
			serveMaindata(w, r)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	c, _ := New(ts.URL, "", "")
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err) // bypass login is a no-op and must succeed
	}
	md, err := c.SyncMaindata(context.Background(), 0)
	if err != nil || !md.FullUpdate || md.RID != 1 || len(md.Torrents) != 1 {
		t.Fatalf("first sync = %+v err=%v", md, err)
	}
	if md.ServerState == nil || md.ServerState.DlInfoSpeed == nil || *md.ServerState.DlInfoSpeed != 10 {
		t.Fatalf("server_state = %+v", md.ServerState)
	}
	// Second call with the session cookie must be a delta with a new rid.
	md2, err := c.SyncMaindata(context.Background(), md.RID)
	if err != nil || md2.FullUpdate || md2.RID != 2 || len(md2.Torrents) != 0 {
		t.Fatalf("delta = %+v err=%v", md2, err)
	}
	if md2.ServerState != nil {
		t.Fatalf("no-change delta carried server_state: %+v", md2.ServerState)
	}
}

func TestSessionExpiryRelogin(t *testing.T) {
	sids := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			sids++
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: fmt.Sprintf("sid-%d", sids), Path: "/"})
			fmt.Fprint(w, "Ok.")
		case "/api/v2/sync/maindata":
			if len(r.Cookies()) == 0 || r.Cookies()[0].Value == "sid-1" {
				w.WriteHeader(http.StatusForbidden) // first session expired
				return
			}
			fmt.Fprint(w, `{"rid":1,"torrents":{}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c, _ := New(ts.URL, "admin", "pw")
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	// First SID expired; fetch must re-login transparently and succeed.
	if _, err := c.SyncMaindata(context.Background(), 0); err != nil {
		t.Fatalf("sync after expiry: %v", err)
	}
	if sids != 2 {
		t.Fatalf("relogins = %d, want 2", sids)
	}
}

func TestUnauthorizedWithoutCredentials(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer ts.Close()

	c, _ := New(ts.URL, "", "")
	_, err := c.SyncMaindata(context.Background(), 0)
	if err != ErrUnauthorized {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

func TestUnexpectedBodyDecode(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html>not json</html>`)
	}))
	defer ts.Close()

	c, _ := New(ts.URL, "", "")
	_, err := c.SyncMaindata(context.Background(), 0)
	if err == nil || !strings.Contains(err.Error(), "unexpected response") {
		t.Fatalf("err = %v, want unexpected response", err)
	}
}

func TestInvalidBaseURL(t *testing.T) {
	if _, err := New("not a url at all", "", ""); err == nil {
		t.Fatal("invalid URL accepted")
	}
	if _, err := New("http://127.0.0.1:8080/", "", ""); err != nil {
		t.Fatalf("trailing slash rejected: %v", err)
	}
	// Credentials embedded in the URL are rejected: they would risk
	// reaching logs (security review finding; docs/SECURITY.md).
	if _, err := New("http://admin:pw@127.0.0.1:8080", "admin", "pw"); err == nil {
		t.Fatal("URL userinfo accepted")
	}
}
