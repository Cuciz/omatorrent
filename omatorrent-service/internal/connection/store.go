// Profile store (ADR-0008 §1): the daemon is the only intended writer.
// Reads refuse symlinks and permissive permissions (fail closed, never
// repaired); writes go through an exclusive-create temp file, fsync and
// an atomic rename inside the same 0700 directory.
package connection

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// MaxProfileBytes bounds the profile file.
const MaxProfileBytes = 128 << 10

// DefaultStorePath is $XDG_CONFIG_HOME/omatorrent/connection.json (or
// the ~/.config fallback).
func DefaultStorePath() (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("connection: no home directory: %w", err)
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "omatorrent", "connection.json"), nil
}

// LoadStore reads and validates the profile. A missing file is not an
// error (ok=false): callers fall back to defaults. Anything present but
// unreadable, permissive, symlinked, oversized, malformed or semantically
// invalid IS an error — the daemon fails closed, it never repairs.
func LoadStore(path string) (Profile, bool, error) {
	data, exists, err := openProfileForRead(path)
	if err != nil || !exists {
		return Profile{}, exists, err
	}

	var p Profile
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Profile{}, true, fmt.Errorf("connection: parse %s: malformed profile: %v", path, err)
	}
	if dec.More() {
		return Profile{}, true, fmt.Errorf("connection: parse %s: trailing content", path)
	}
	if _, err := p.Validate(); err != nil {
		return Profile{}, true, fmt.Errorf("connection: %s: invalid profile: %v", path, err)
	}
	return p, true, nil
}

// ReadStoreRaw returns the raw persisted bytes (same safety checks as
// LoadStore via the shared open helper) so a Configure transaction can
// restore the EXACT pre-transaction persisted state on failure
// (external review round 2, blocker 2). exists=false only for a
// missing file; anything unreadable is an error — never silently
// treated as absent.
func ReadStoreRaw(path string) ([]byte, bool, error) {
	return openProfileForRead(path)
}

// openProfileForRead is the ONE read path for the profile file
// (architecture re-review F5: LoadStore and ReadStoreRaw share it, so
// their safety checks cannot drift): symlinked directory components
// refused, O_NOFOLLOW open, fstat permission/regular checks on the
// handle, bounded size.
func openProfileForRead(path string) (data []byte, exists bool, err error) {
	if err := refuseSymlinkedDir(filepath.Dir(path)); err != nil {
		return nil, true, err
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		if errors.Is(err, syscall.ELOOP) {
			return nil, true, fmt.Errorf("connection: refusing symlinked profile %s", path)
		}
		return nil, true, fmt.Errorf("connection: open %s: %w", path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, true, fmt.Errorf("connection: stat %s: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, true, fmt.Errorf("connection: %s must not be group/world accessible (mode %04o)", path, info.Mode().Perm())
	}
	if !info.Mode().IsRegular() {
		return nil, true, fmt.Errorf("connection: %s is not a regular file", path)
	}
	data, err = io.ReadAll(io.LimitReader(f, MaxProfileBytes+1))
	if err != nil {
		return nil, true, fmt.Errorf("connection: read %s: %w", path, err)
	}
	if len(data) > MaxProfileBytes {
		return nil, true, fmt.Errorf("connection: %s exceeds %d bytes", path, MaxProfileBytes)
	}
	return data, true, nil
}

// SaveStore validates and atomically writes the profile (see
// SaveStoreRaw for the write mechanics).
func SaveStore(path string, p Profile) error {
	if _, err := p.Validate(); err != nil {
		return fmt.Errorf("connection: refusing to save invalid profile: %w", err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("connection: encode profile: %w", err)
	}
	data = append(data, '\n')
	return SaveStoreRaw(path, data)
}

// SaveStoreRaw atomically writes raw (previously validated) bytes:
// 0700 directory (created if missing, never repaired), exclusive-create
// 0600 temp file in the same directory, fsync, rename over the target
// (a symlink at the target is replaced, not followed), directory fsync.
func SaveStoreRaw(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := ensurePrivateDir(dir); err != nil {
		return err
	}

	oldMask := syscall.Umask(0o077)
	tmp, err := os.OpenFile(filepath.Join(dir, ".connection.json.tmp"),
		os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	syscall.Umask(oldMask)
	if err != nil {
		return fmt.Errorf("connection: create temp profile: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("connection: write temp profile: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("connection: sync temp profile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("connection: close temp profile: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("connection: rename profile into place: %w", err)
	}
	syncDir(dir)
	return nil
}

// refuseSymlinkedDir walks every component of dir and refuses if any
// is a symlink (the file itself is separately O_NOFOLLOW-guarded).
func refuseSymlinkedDir(dir string) error {
	components := strings.Split(strings.Trim(filepath.Clean(dir), string(os.PathSeparator)), string(os.PathSeparator))
	cur := string(os.PathSeparator)
	for _, c := range components {
		if c == "" {
			continue
		}
		cur = filepath.Join(cur, c)
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("connection: refusing symlinked path component %s", cur)
		}
	}
	return nil
}

// ensurePrivateDir validates the profile directory: every component
// from the config root down must be symlink-free; the final component
// is created 0700 when missing and must be 0700, UID-owned and a real
// directory when present (never repaired).
func ensurePrivateDir(dir string) error {
	root := configRoot(dir) // e.g. ~/.config — must exist; symlinked
	// components BELOW it are refused by the walk above
	components := strings.Split(strings.TrimPrefix(dir, root), string(os.PathSeparator))
	cur := root
	for _, c := range components {
		if c == "" {
			continue
		}
		cur = filepath.Join(cur, c)
		fi, err := os.Lstat(cur)
		switch {
		case errors.Is(err, os.ErrNotExist):
			if err := os.Mkdir(cur, 0o700); err != nil {
				return fmt.Errorf("connection: create %s: %w", cur, err)
			}
			return nil // deeper components only exist after this creation
		case err != nil:
			return fmt.Errorf("connection: inspect %s: %w", cur, err)
		case fi.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("connection: refusing symlinked path component %s", cur)
		case !fi.IsDir():
			return fmt.Errorf("connection: refusing non-directory %s", cur)
		}
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("connection: inspect %s: %w", dir, err)
	}
	if fi.Mode().Perm() != 0o700 {
		return fmt.Errorf("connection: refusing directory %s with mode %04o (want 0700)", dir, fi.Mode().Perm())
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("connection: refusing directory %s not owned by uid %d", dir, os.Getuid())
	}
	return nil
}

// configRoot returns the ancestor directory that must already exist
// (~/.config or $XDG_CONFIG_HOME itself). For a default path this is
// two levels up from the file (…/omatorrent/connection.json).
func configRoot(dir string) string {
	return filepath.Dir(filepath.Dir(dir))
}

func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	d.Sync()
	d.Close()
}
