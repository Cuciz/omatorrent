// omatorrent-service is the OmaTorrent daemon (ADR-0001/0002): it owns
// all qBittorrent communication and serves IPC v1 (ADR-0003/0004) on a
// Unix-domain socket for presentation layers.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Cuciz/omatorrent/omatorrent-service/internal/config"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/ipc"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/qbittorrent"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/state"
)

const version = "0.1.0-phase0"

func main() {
	var configPath, socketOverride string
	flag.StringVar(&configPath, "config", "", "explicit config file path (default: $XDG_CONFIG_HOME/omatorrent/service.json)")
	flag.StringVar(&socketOverride, "socket", "", "override IPC socket path (absolute; testing/development)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(log, configPath, socketOverride); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, configPath, socketOverride string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if socketOverride != "" {
		cfg.IPC.SocketPath = socketOverride
	}

	qbt, err := qbittorrent.New(cfg.QBittorrent.URL, cfg.QBittorrent.Username, cfg.QBittorrent.Password)
	if err != nil {
		return err
	}
	mgr := state.New(qbt, state.Options{}, log)

	socketPath, err := ipc.ResolveSocketPath(cfg.IPC.SocketPath)
	if err != nil {
		return err
	}
	srv, err := ipc.New(socketPath, &daemonHandler{mgr: mgr}, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()
	go mgr.Run(ctx)

	log.Info("omatorrent-service started",
		"version", version,
		"socket", socketPath,
		"qbittorrent_url", cfg.QBittorrent.URL, // endpoint only; never credentials
	)

	select {
	case err := <-serveErr:
		if err != nil {
			srv.Close()
			return err
		}
	case <-ctx.Done():
	}

	log.Info("omatorrent-service stopping")
	srv.Close()
	stop()
	log.Info("omatorrent-service stopped")
	return nil
}

// daemonHandler adapts the state manager to the IPC handler interface.
type daemonHandler struct {
	mgr *state.Manager
}

func (h *daemonHandler) Health() bool { return h.mgr.Health() }

func (h *daemonHandler) StatusData() (ipc.StatusData, bool) {
	snap := h.mgr.Snapshot()
	return ipc.StatusData{
		AppVersion:    snap.AppVersion,
		WebAPIVersion: snap.WebAPIVersion,
		DlSpeed:       snap.DlSpeed,
		UpSpeed:       snap.UpSpeed,
		TorrentsTotal: snap.TorrentsTotal,
	}, snap.QBittorrentOK
}
