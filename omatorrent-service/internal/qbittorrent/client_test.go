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
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: "s3cret"})
			fmt.Fprint(w, "Ok.")
			return
		}
		if r.URL.Path == "/api/v2/app/version" {
			if r.Cookies()[0].Name != "SID" {
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
	_, err := c.TransferInfo(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("err = %v, want unreachable", err)
	}
}

func TestBypassWithoutCredentials(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/transfer/info":
			fmt.Fprint(w, `{"connection_status":"connected","dl_info_speed":100,"up_info_speed":200}`)
		case "/api/v2/torrents/info":
			fmt.Fprint(w, `[{"hash":"a"},{"hash":"b"}]`)
		case "/api/v2/app/webapiVersion":
			fmt.Fprint(w, "2.15.1")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c, _ := New(ts.URL, "", "")
	info, err := c.TransferInfo(context.Background())
	if err != nil || info.DlSpeed != 100 || info.UpSpeed != 200 || info.Status != "connected" {
		t.Fatalf("info = %+v err=%v", info, err)
	}
	n, err := c.TorrentsCount(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("count = %d err=%v", n, err)
	}
	api, err := c.WebAPIVersion(context.Background())
	if err != nil || api != "2.15.1" {
		t.Fatalf("webapiVersion = %q err=%v", api, err)
	}
	// Login with no username must be a no-op.
	if err := c.Login(context.Background()); err != nil {
		t.Fatalf("bypass login: %v", err)
	}
}

func TestSessionExpiryRelogin(t *testing.T) {
	sids := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			sids++
			http.SetCookie(w, &http.Cookie{Name: "SID", Value: fmt.Sprintf("sid-%d", sids)})
			fmt.Fprint(w, "Ok.")
		case "/api/v2/transfer/info":
			if len(r.Cookies()) == 0 || r.Cookies()[0].Value == "sid-1" {
				w.WriteHeader(http.StatusForbidden) // first session expired
				return
			}
			fmt.Fprint(w, `{"dl_info_speed":1,"up_info_speed":2,"connection_status":"connected"}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	c, _ := New(ts.URL, "admin", "pw")
	if err := c.Login(context.Background()); err != nil {
		t.Fatal(err)
	}
	// First SID is expired; get must re-login transparently and succeed.
	info, err := c.TransferInfo(context.Background())
	if err != nil || info.DlSpeed != 1 {
		t.Fatalf("info = %+v err=%v", info, err)
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
	_, err := c.TransferInfo(context.Background())
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
	_, err := c.TransferInfo(context.Background())
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
}
