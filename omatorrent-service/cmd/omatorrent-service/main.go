// omatorrent-service is the OmaTorrent daemon (ADR-0001/0002): it owns
// all qBittorrent communication (incremental sync/maindata since 0.2)
// and serves IPC v1/v1.1 (ADR-0003/0004/0005) on a Unix-domain socket
// for presentation layers.
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

const version = "0.2.0-phase02"

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
	syncer := state.New(qbt, state.Options{}, log)

	socketPath, err := ipc.ResolveSocketPath(cfg.IPC.SocketPath)
	if err != nil {
		return err
	}
	handler := &daemonHandler{syncer: syncer}
	srv, err := ipc.New(socketPath, handler, handler, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()
	go syncer.Run(ctx)

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

// daemonHandler adapts the state syncer to the IPC interfaces: v1.0
// status responses and v1.1 torrent subscriptions.
type daemonHandler struct {
	syncer *state.Syncer
}

func (h *daemonHandler) Health() bool { return h.syncer.Health() }

func (h *daemonHandler) StatusData() (ipc.StatusData, bool) {
	st := h.syncer.State()
	return ipc.StatusData{
		AppVersion:    st.AppVersion,
		WebAPIVersion: st.WebAPIVersion,
		DlSpeed:       st.DlSpeed,
		UpSpeed:       st.UpSpeed,
		TorrentsTotal: len(st.Torrents),
	}, st.BackendOK
}

// Subscribe implements ipc.Subscriptions from the syncer's committed
// state and change events. Forwarding is bounded: a slow IPC consumer
// is dropped (closed channel) per the ADR-0005 slow-consumer rule.
func (h *daemonHandler) Subscribe() ([]ipc.TorrentItem, <-chan ipc.DeltaEvent, func()) {
	st, events, cancel := h.syncer.Subscribe()
	items := make([]ipc.TorrentItem, 0, len(st.Torrents))
	for _, t := range st.Torrents {
		items = append(items, toItem(t))
	}
	ch := make(chan ipc.DeltaEvent, 16)
	go func() {
		defer close(ch)
		for ev := range events {
			out := ipc.DeltaEvent{Seq: ev.Seq, Removed: ev.Removed}
			for _, t := range ev.Changed {
				out.Changed = append(out.Changed, toItem(t))
			}
			select {
			case ch <- out:
			default:
				return // consumer too slow; drop the subscription
			}
		}
	}()
	return items, ch, cancel
}

func toItem(t state.Torrent) ipc.TorrentItem {
	return ipc.TorrentItem{
		Hash:      t.Hash,
		Name:      t.Name,
		State:     t.State,
		Progress:  t.Progress,
		DlSpeed:   t.DlSpeed,
		UpSpeed:   t.UpSpeed,
		Eta:       t.Eta,
		Ratio:     t.Ratio,
		Category:  t.Category,
		Size:      t.Size,
		Completed: t.Completed,
	}
}
