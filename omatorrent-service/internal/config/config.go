// Package config loads omatorrent-service configuration.
//
// Phase 0 shape: a single JSON file holding the qBittorrent endpoint and
// optional credentials, plus an optional IPC socket path override. The file
// must not be group/world readable when it exists (fail closed). A real
// secret provider (systemd LoadCredential / libsecret) is future work; the
// restricted-permission file is the Phase 0 credential source and secrets
// from it are never logged.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// QBittorrent is the backend endpoint configuration.
type QBittorrent struct {
	// URL is the WebUI base URL, e.g. "http://127.0.0.1:8080".
	URL string `json:"url"`
	// Username and Password are optional; when Username is empty the
	// adapter relies on the qBittorrent localhost auth bypass.
	Username string `json:"username,omitempty"`
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

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && explicit == "" && os.Getenv("OMATORRENT_CONFIG") == "" {
			return cfg, nil
		}
		return cfg, fmt.Errorf("config: read %s: %w", path, err)
	}

	info, err := os.Stat(path)
	if err != nil {
		return cfg, fmt.Errorf("config: stat %s: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return cfg, fmt.Errorf("config: %s: %w (mode %04o)", path, ErrInsecureConfig, info.Mode().Perm())
	}

	var fileCfg Config
	if err := json.Unmarshal(data, &fileCfg); err != nil {
		return cfg, fmt.Errorf("config: parse %s: %w", path, err)
	}
	if fileCfg.QBittorrent.URL != "" {
		cfg.QBittorrent.URL = fileCfg.QBittorrent.URL
	}
	cfg.QBittorrent.Username = fileCfg.QBittorrent.Username
	cfg.QBittorrent.Password = fileCfg.QBittorrent.Password
	cfg.IPC.SocketPath = fileCfg.IPC.SocketPath
	return cfg, nil
}
