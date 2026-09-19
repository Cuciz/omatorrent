package secrets

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeTool writes a secret-tool-compatible script: store reads the
// secret from stdin into a file; lookup prints it (with a trailing
// newline, like the real tool observed in research); clear removes it.
func fakeTool(t *testing.T) (bin string, dir string) {
	t.Helper()
	dir = t.TempDir()
	state := filepath.Join(dir, "item")
	bin = filepath.Join(dir, "secret-tool")
	script := "#!/bin/sh\n" +
		"case \"$1\" in\n" +
		"  store) cat > " + state + " ;;\n" +
		"  lookup) [ -f " + state + " ] && { cat " + state + "; printf '\\n'; } ;;\n" +
		"  clear) rm -f " + state + " ;;\n" +
		"  *) exit 8 ;;\n" +
		"esac\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func TestSecretToolRoundTrip(t *testing.T) {
	bin, _ := fakeTool(t)
	p := NewSecretToolAt(bin)
	ctx := context.Background()

	if err := p.Store(ctx, []byte("hunter2")); err != nil {
		t.Fatalf("store: %v", err)
	}
	got, ok, err := p.Get(ctx)
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	// The fake mimics the real tool's trailing-newline stdout behavior;
	// Get must strip at most one so the secret round-trips exactly.
	if string(got) != "hunter2" {
		t.Fatalf("secret = %q", got)
	}
	if err := p.Delete(ctx); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := p.Get(ctx); err != nil || ok {
		t.Fatalf("after delete: ok=%v err=%v", ok, err)
	}
}

// A password legitimately ending in a newline keeps it (the store
// contract: stdin bytes are the secret; only the tool's own trailing
// newline is stripped — at most one).
func TestSecretToolPasswordWithTrailingNewline(t *testing.T) {
	bin, _ := fakeTool(t)
	p := NewSecretToolAt(bin)
	ctx := context.Background()
	if err := p.Store(ctx, []byte("line1\n")); err != nil {
		t.Fatal(err)
	}
	got, ok, _ := p.Get(ctx)
	if !ok || string(got) != "line1\n" {
		t.Fatalf("secret = %q ok=%v", got, ok)
	}
}

func TestSecretToolUnavailable(t *testing.T) {
	p := NewSecretToolAt("/nonexistent/secret-tool")
	_, _, err := p.Get(context.Background())
	if err == nil || !strings.Contains(err.Error(), "unavailable") {
		t.Fatalf("err = %v, want ErrUnavailable-classified", err)
	}
}

func TestSecretToolRefusesEmptySecret(t *testing.T) {
	bin, _ := fakeTool(t)
	if err := NewSecretToolAt(bin).Store(context.Background(), nil); err == nil {
		t.Fatal("empty secret accepted")
	}
}

func TestFakeProvider(t *testing.T) {
	f := &Fake{}
	ctx := context.Background()
	if _, ok, _ := f.Get(ctx); ok {
		t.Fatal("empty fake reports a secret")
	}
	f.Store(ctx, []byte("s1"))
	got, ok, _ := f.Get(ctx)
	if !ok || string(got) != "s1" {
		t.Fatalf("get = %q ok=%v", got, ok)
	}
	f.Delete(ctx)
	if _, ok, _ := f.Get(ctx); ok {
		t.Fatal("delete did not clear")
	}
	f.Unavailable = true
	if _, _, err := f.Get(ctx); err == nil {
		t.Fatal("unavailable fake returned nil error")
	}
}

func TestWipe(t *testing.T) {
	b := []byte("secret")
	Wipe(b)
	for i, c := range b {
		if c != 0 {
			t.Fatalf("byte %d not wiped", i)
		}
	}
}
