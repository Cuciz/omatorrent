// Package config loads omatorrent-service configuration.
//
// A single JSON file holding the static service configuration: the
// fallback qBittorrent endpoint (no credentials — passwords are
// rejected since Phase 0.5, ADR-0009) and an optional IPC socket path
// override. The file must not be group/world readable when it exists
// (fail closed). The runtime connection profile (remote backends,
// TLS policy) lives in the daemon-owned connection store
// (internal/connection).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// QBittorrent is the legacy endpoint configuration. Since Phase 0.5
// (ADR-0009) the password field is REMOVED: a config still carrying a
// non-empty password fails load with an explicit migration error —
// credentials live in the Secret Service, configured via the settings
// surface. URL/Username here remain the fallback connection profile
// for setups that never configured one via IPC.
type QBittorrent struct {
	// URL is the WebUI base URL, e.g. "http://127.0.0.1:8080".
	URL string `json:"url"`
	// Username is optional; when empty the adapter relies on the
	// qBittorrent localhost auth bypass.
	Username string `json:"username,omitempty"`
	// Password is rejected at load (migration error). Kept in the
	// struct only so the rejection can name the field.
	Password string `json:"password,omitempty"`
}

// IPC is the IPC listener configuration.
type IPC struct {
	// SocketPath overrides $XDG_RUNTIME_DIR/omatorrent/service.sock.
	// Empty means the default. Used by tests and development.
	SocketPath string `json:"socket_path,omitempty"`
}

// Config is the daemon configuration root.
type Config struct {
	QBittorrent QBittorrent `json:"qbittorrent"`
	IPC         IPC         `json:"ipc"`
}

// Defaults returns the development defaults for this workstation.
func Defaults() Config {
	return Config{
		QBittorrent: QBittorrent{
			URL: "http://127.0.0.1:8080",
		},
	}
}

// ErrInsecureConfig reports a config file with permissive permissions.
var ErrInsecureConfig = errors.New("config file must not be group/world accessible")

// Load reads the configuration. Explicit paths: flag value, else
// $OMATORRENT_CONFIG, else $XDG_CONFIG_HOME/omatorrent/service.json (or
// ~/.config fallback). A missing file at the default location is not an
// error (defaults apply); a missing explicitly-requested file is.
func Load(explicit string) (Config, error) {
	cfg := Defaults()

	path := explicit
	if path == "" {
		path = os.Getenv("OMATORRENT_CONFIG")
	}
	if path == "" {
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return cfg, fmt.Errorf("config: no home directory: %w", err)
			}
			dir = filepath.Join(home, ".config")
		}
		path = filepath.Join(dir, "omatorrent", "service.json")
	}

	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && explicit == "" && os.Getenv("OMATORRENT_CONFIG") == "" {
			return cfg, nil
		}
		return cfg, fmt.Errorf("config: open %s: %w", path, err)
	}
	defer f.Close()

	// Stat the handle (not the path) so the permission check and the read
	// see the same file; O_NOFOLLOW above refuses symlinked configs.
	info, err := f.Stat()
	if err != nil {
		return cfg, fmt.Errorf("config: stat %s: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return cfg, fmt.Errorf("config: %s: %w (mode %04o)", path, ErrInsecureConfig, info.Mode().Perm())
	}

	data, err := io.ReadAll(io.LimitReader(f, 1<<20))
	if err != nil {
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}

	var fileCfg Config
	if err := json.Unmarshal(data, &fileCfg); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if fileCfg.QBittorrent.Password != "" {
		// Fail closed: no silent acceptance of plaintext credentials
		// (ADR-0009). Local-bypass setups carry no password; remote
		// users re-enter it once via the connection settings surface.
		return cfg, fmt.Errorf("config: %s: the qbittorrent.password field is no longer supported (stored in the Secret Service since Phase 0.5) — remove it and configure credentials via OmaTorrent's connection settings", path)
	}
	if fileCfg.QBittorrent.URL != "" {
		cfg.QBittorrent.URL = fileCfg.QBittorrent.URL
	}
	cfg.QBittorrent.Username = fileCfg.QBittorrent.Username
	cfg.IPC.SocketPath = fileCfg.IPC.SocketPath
	return cfg, nil
}
