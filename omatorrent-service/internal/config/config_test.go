package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultsWhenNoFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("OMATORRENT_CONFIG", "")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.QBittorrent.URL != "http://127.0.0.1:8080" {
		t.Fatalf("default URL = %q", cfg.QBittorrent.URL)
	}
	if cfg.QBittorrent.Username != "" || cfg.QBittorrent.Password != "" {
		t.Fatal("default credentials not empty")
	}
	if cfg.IPC.SocketPath != "" {
		t.Fatalf("default socket override = %q", cfg.IPC.SocketPath)
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.json")
	body := `{"qbittorrent":{"url":"http://127.0.0.1:9999","username":"u"},"ipc":{"socket_path":"/tmp/x.sock"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.QBittorrent.URL != "http://127.0.0.1:9999" || cfg.QBittorrent.Username != "u" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if cfg.QBittorrent.Password != "" {
		t.Fatal("password field must never populate the config")
	}
	if cfg.IPC.SocketPath != "/tmp/x.sock" {
		t.Fatalf("socket = %q", cfg.IPC.SocketPath)
	}
}

// Phase 0.5 (ADR-0009): a config still carrying a plaintext password
// fails load with an explicit migration error — fail closed, never a
// silent acceptance.
func TestRejectsLegacyPassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.json")
	body := `{"qbittorrent":{"url":"http://127.0.0.1:9999","username":"u","password":"p"}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), "password") {
		t.Fatalf("err = %v, want migration error naming the password field", err)
	}
}

func TestRejectsPermissiveFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "service.json")
	os.WriteFile(path, []byte(`{}`), 0o644)
	_, err := Load(path)
	if !errors.Is(err, ErrInsecureConfig) {
		t.Fatalf("err = %v, want ErrInsecureConfig", err)
	}
}

func TestExplicitMissingFileIsError(t *testing.T) {
	_, err := Load("/nonexistent/omatorrent/service.json")
	if err == nil {
		t.Fatal("missing explicit config accepted")
	}
}

// A symlinked config is refused (O_NOFOLLOW), even when the target has
// safe permissions.
func TestRejectsSymlinkedConfig(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	os.WriteFile(real, []byte(`{}`), 0o600)
	link := filepath.Join(dir, "service.json")
	os.Symlink(real, link)
	if _, err := Load(link); err == nil {
		t.Fatal("symlinked config accepted")
	}
}

func TestEnvVarPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")
	os.WriteFile(path, []byte(`{"qbittorrent":{"url":"http://x:1"}}`), 0o600)
	t.Setenv("OMATORRENT_CONFIG", path)
	cfg, err := Load("")
	if err != nil || cfg.QBittorrent.URL != "http://x:1" {
		t.Fatalf("cfg = %+v err=%v", cfg, err)
	}
}
