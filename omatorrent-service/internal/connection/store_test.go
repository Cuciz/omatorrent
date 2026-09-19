package connection

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func writeProfile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// profilePath places the profile in its own directory, mirroring the
// real ~/.config/omatorrent/connection.json layout (SaveStore enforces
// 0700 on the profile's own directory).
func profilePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "omatorrent", "connection.json")
}

func TestStoreRoundTrip(t *testing.T) {
	path := profilePath(t)
	p := Profile{URL: "https://qbittorrent.home.arpa", Username: "clement",
		TLSMode: TLSSystem, AllowInsecureHTTP: false}
	if err := SaveStore(path, p); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, exists, err := LoadStore(path)
	if err != nil || !exists {
		t.Fatalf("load: exists=%v err=%v", exists, err)
	}
	if got != p {
		t.Fatalf("round trip = %+v, want %+v", got, p)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %04o", info.Mode().Perm())
	}
	// No temp file residue.
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".connection") {
			t.Fatalf("temp file residue: %s", e.Name())
		}
	}
}

func TestStoreMissingIsNotError(t *testing.T) {
	_, exists, err := LoadStore(filepath.Join(t.TempDir(), "connection.json"))
	if err != nil || exists {
		t.Fatalf("exists=%v err=%v", exists, err)
	}
}

func TestStoreRejectsPermissive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connection.json")
	writeProfile(t, path, `{"url":"http://127.0.0.1:8080","tls_mode":"system"}`, 0o644)
	if _, _, err := LoadStore(path); err == nil {
		t.Fatal("permissive profile accepted")
	}
}

func TestStoreRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.json")
	writeProfile(t, real, `{}`, 0o600)
	link := filepath.Join(dir, "connection.json")
	os.Symlink(real, link)
	if _, _, err := LoadStore(link); err == nil {
		t.Fatal("symlinked profile accepted")
	}
}

func TestStoreRejectsMalformed(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      `{"url":`,
		"unknown field": `{"url":"http://127.0.0.1:8080","tls_mode":"system","evil":1}`,
		"invalid url":   `{"url":"ftp://x","tls_mode":"system"}`,
		"bad tls mode":  `{"url":"http://127.0.0.1:8080","tls_mode":"nope"}`,
		"trailing":      `{"url":"http://127.0.0.1:8080"} {}`,
	} {
		path := filepath.Join(t.TempDir(), "connection.json")
		writeProfile(t, path, body, 0o600)
		if _, _, err := LoadStore(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestStoreRejectsOversized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connection.json")
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write(make([]byte, MaxProfileBytes+1))
	f.Close()
	if _, _, err := LoadStore(path); err == nil {
		t.Fatal("oversized profile accepted")
	}
}

func TestSaveStoreRefusesInvalid(t *testing.T) {
	path := profilePath(t)
	if err := SaveStore(path, Profile{URL: "ftp://x", TLSMode: TLSSystem}); err == nil {
		t.Fatal("invalid profile saved")
	}
}

// The store directory is created 0700; a permissive existing directory
// is refused, never repaired.
func TestSaveStoreDirectoryDiscipline(t *testing.T) {
	t.Run("creates 0700", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "omatorrent", "connection.json")
		if err := SaveStore(path, DefaultProfile()); err != nil {
			t.Fatalf("save: %v", err)
		}
		info, _ := os.Stat(filepath.Dir(path))
		if info.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode = %04o", info.Mode().Perm())
		}
	})
	t.Run("refuses permissive", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "omatorrent")
		os.Mkdir(dir, 0o755)
		if err := SaveStore(filepath.Join(dir, "connection.json"), DefaultProfile()); err == nil {
			t.Fatal("permissive directory accepted")
		}
	})
	t.Run("refuses symlinked dir", func(t *testing.T) {
		root := t.TempDir()
		real := filepath.Join(root, "real")
		os.Mkdir(real, 0o700)
		link := filepath.Join(root, "omatorrent")
		os.Symlink(real, link)
		if err := SaveStore(filepath.Join(link, "connection.json"), DefaultProfile()); err == nil {
			t.Fatal("symlinked directory accepted")
		}
	})
}

// Write-rename over a symlinked target replaces the symlink (rename
// does not follow) — the daemon never writes through a symlink.
func TestSaveStoreReplacesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	os.MkdirAll(filepath.Join(dir, "victim"), 0o700)
	victim := filepath.Join(dir, "victim", "connection.json")
	writeProfile(t, victim, "sentinel", 0o600)
	path := filepath.Join(dir, "connection.json")
	os.Symlink(victim, path)
	if err := SaveStore(path, DefaultProfile()); err != nil {
		t.Fatalf("save: %v", err)
	}
	// The victim file is untouched; the symlink was replaced by a
	// regular file.
	data, _ := os.ReadFile(victim)
	if string(data) != "sentinel" {
		t.Fatal("write followed the symlink")
	}
	info, _ := os.Lstat(path)
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatal("target is still a symlink")
	}
}

func TestRemoveStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connection.json")
	writeProfile(t, path, "{}", 0o600)
	if err := removeStore(path); err != nil {
		t.Fatal(err)
	}
	if err := removeStore(path); err != nil { // missing is fine
		t.Fatal(err)
	}
}

// LoadActive prefers the store, falls back cleanly.
func TestLoadActive(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "connection.json")

	p, _, err := LoadActive(path, DefaultProfile())
	if err != nil || p.URL != DefaultProfile().URL {
		t.Fatalf("fallback: %+v err=%v", p, err)
	}
	writeProfile(t, path, `{"url":"https://qb.example","tls_mode":"system"}`, 0o600)
	p, _, err = LoadActive(path, DefaultProfile())
	if err != nil || p.URL != "https://qb.example" {
		t.Fatalf("store: %+v err=%v", p, err)
	}
	// Malformed store fails closed even with a valid fallback.
	writeProfile(t, path, `{`, 0o600)
	if _, _, err := LoadActive(path, DefaultProfile()); err == nil {
		t.Fatal("malformed store accepted")
	}
}

// The temp-file write must be exclusive-create: a leftover temp file
// (e.g. from a crash) must not be clobbered blindly.
func TestSaveStoreTempCollision(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0o700)
	tmp := filepath.Join(dir, ".connection.json.tmp")
	writeProfile(t, tmp, "leftover", 0o600)
	path := filepath.Join(dir, "connection.json")
	err := SaveStore(path, DefaultProfile())
	if err == nil {
		t.Fatal("existing temp file silently overwritten (want O_EXCL refusal)")
	}
	if _, statErr := os.Stat(tmp); statErr != nil || func() bool { b, _ := os.ReadFile(tmp); return string(b) != "leftover" }() {
		t.Fatal("leftover temp file damaged")
	}
	_ = syscall.ENOENT // keep import parity with store.go conventions
}
