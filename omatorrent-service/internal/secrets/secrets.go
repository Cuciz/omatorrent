// Package secrets is the daemon's only credential holder (ADR-0009).
// The production provider talks to the freedesktop Secret Service via
// the `secret-tool` subprocess: the secret crosses stdin (store) or
// stdout (lookup) pipes only — never argv, never temp files, matching
// Omarchy's own secret-transport rule. There is NO plaintext fallback;
// a missing/locked provider is a degraded state, never a prompt loop.
package secrets

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Attribute keys identifying OmaTorrent's item in the Secret Service.
const (
	AttrService = "omatorrent"
	AttrKind    = "qbt-webui-password"
	// itemLabel is pure ASCII on purpose: GLib converts labels from the
	// locale charset, and the daemon's minimal child environment may run
	// under C/POSIX locale where non-ASCII fails conversion (live-
	// verified: "Invalid byte sequence in conversion input", exit 2).
	itemLabel = "OmaTorrent qBittorrent WebUI password"
)

// ErrUnavailable reports a missing/unusable secret store (secret-tool
// absent, collection locked, D-Bus unreachable). It is a state, not a
// bug: callers degrade truthfully.
var ErrUnavailable = errors.New("secrets: secret store unavailable")

// Provider stores and retrieves one credential. Implementations must
// be safe for concurrent use.
type Provider interface {
	// Store writes the secret, replacing any previous value.
	Store(ctx context.Context, secret []byte) error
	// Get returns the stored secret; ok=false when no secret is stored.
	// An unusable store is ErrUnavailable (or a wrapped error), never a
	// wrong secret.
	Get(ctx context.Context) (secret []byte, ok bool, err error)
	// Delete removes the stored secret; a missing secret is not an error.
	Delete(ctx context.Context) error
}

// opTimeout bounds every secret-tool subprocess.
const opTimeout = 5 * time.Second

// SecretTool is the production Provider (ADR-0009). binPath is
// overridable for tests; empty means "secret-tool from PATH".
type SecretTool struct {
	binPath string
}

// NewSecretTool returns the production provider.
func NewSecretTool() *SecretTool { return &SecretTool{} }

// NewSecretToolAt returns a provider invoking the given secret-tool
// binary (tests point this at a fake script).
func NewSecretToolAt(bin string) *SecretTool { return &SecretTool{binPath: bin} }

func (s *SecretTool) tool() string {
	if s.binPath != "" {
		return s.binPath
	}
	return "secret-tool"
}

// run executes one secret-tool operation. argv carries only the label
// and attribute pairs; the secret (store's stdin, lookup's stdout)
// never appears on a command line.
func (s *SecretTool) run(ctx context.Context, args []string, stdin []byte) (stdout []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.tool(), args...)
	// Minimal environment: the Secret Service lives on the user's
	// session bus; nothing else is needed, and a reduced env shrinks
	// surprise leakage surface.
	cmd.Env = minimalEnv()
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	// stderr is deliberately discarded: subprocess diagnostics are not
	// logged verbatim (they could echo parts of the operation context);
	// the classified error below is the whole report.
	cmd.Stderr = nil

	if err := cmd.Run(); err != nil {
		// Keep the classified error chainable (ErrUnavailable) while
		// preserving the ExitError so Get can recognize the documented
		// exit-1 "no match" case.
		return out.Bytes(), fmt.Errorf("%w: secret-tool %s: %w", ErrUnavailable, args[0], err)
	}
	return out.Bytes(), nil
}

func minimalEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		for _, keep := range []string{"PATH=", "HOME=", "DBUS_SESSION_BUS_ADDRESS=", "XDG_RUNTIME_DIR=", "LANG=", "LC_ALL=", "LC_CTYPE="} {
			if strings.HasPrefix(kv, keep) {
				env = append(env, kv)
			}
		}
	}
	return env
}

func (s *SecretTool) Store(ctx context.Context, secret []byte) error {
	if len(secret) == 0 {
		return errors.New("secrets: refusing to store an empty secret")
	}
	_, err := s.run(ctx, append([]string{"store", "--label=" + itemLabel}, attrArgs()...), secret)
	return err
}

// Get returns the stored secret. secret-tool's documented "no match"
// answer is exit 1 with empty output (research 2026-09-19) — that is
// ok=false with a nil error; any other failure is an unusable store.
func (s *SecretTool) Get(ctx context.Context) ([]byte, bool, error) {
	stdout, err := s.run(ctx, append([]string{"lookup"}, attrArgs()...), nil)
	if err == nil {
		out := stdout
		if len(out) > 0 && out[len(out)-1] == '\n' {
			out = out[:len(out)-1] // strip at most ONE trailing newline
		}
		if len(out) == 0 {
			return nil, false, nil
		}
		return out, true, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 && len(stdout) == 0 {
		return nil, false, nil // not stored
	}
	return nil, false, err
}

func (s *SecretTool) Delete(ctx context.Context) error {
	_, err := s.run(ctx, append([]string{"clear"}, attrArgs()...), nil)
	return err
}

// attrArgs is the attribute pair list identifying OmaTorrent's item
// (secret-tool's `attribute value` form): service=omatorrent,
// kind=qbt-webui-password.
func attrArgs() []string {
	return []string{"service", AttrService, "kind", AttrKind}
}

// Fake is an in-memory Provider for unit tests. It can be told to
// simulate an unusable store (ErrUnavailable on every operation). It is
// safe for concurrent use (tests flip Secret/Unavailable while the
// Manager's background presence probe runs).
type Fake struct {
	mu          sync.Mutex
	Secret      []byte
	Unavailable bool
	StoreCalls  int
	GetCalls    int
	DelCalls    int
}

func (f *Fake) Store(ctx context.Context, secret []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.StoreCalls++
	if f.Unavailable {
		return ErrUnavailable
	}
	cp := make([]byte, len(secret))
	copy(cp, secret)
	f.Secret = cp
	return nil
}

func (f *Fake) Get(ctx context.Context) ([]byte, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.GetCalls++
	if f.Unavailable {
		return nil, false, ErrUnavailable
	}
	if f.Secret == nil {
		return nil, false, nil
	}
	cp := make([]byte, len(f.Secret))
	copy(cp, f.Secret)
	return cp, true, nil
}

func (f *Fake) Delete(ctx context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.DelCalls++
	if f.Unavailable {
		return ErrUnavailable
	}
	f.Secret = nil
	return nil
}

// SecretValue snapshots the stored secret (test assertions).
func (f *Fake) SecretValue() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Secret == nil {
		return nil
	}
	return append([]byte(nil), f.Secret...)
}

// SetSecret replaces the stored secret atomically (tests: flipping the
// field directly would race the Manager's background presence probe).
func (f *Fake) SetSecret(secret []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if secret == nil {
		f.Secret = nil
		return
	}
	f.Secret = append([]byte(nil), secret...)
}

// SetUnavailable toggles the unusable-store simulation atomically.
func (f *Fake) SetUnavailable(v bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Unavailable = v
}

// Wipe zeroes a secret buffer (best-effort hygiene; strings cannot be
// zeroed — the adapter therefore handles credentials as []byte).
func Wipe(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
