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
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/connection"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/ipc"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/mutate"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/secrets"
	"github.com/Cuciz/omatorrent/omatorrent-service/internal/state"
)

const version = "0.5.0-phase05"

func main() {
	var configPath, socketOverride, connectionPath string
	flag.StringVar(&configPath, "config", "", "explicit config file path (default: $XDG_CONFIG_HOME/omatorrent/service.json)")
	flag.StringVar(&socketOverride, "socket", "", "override IPC socket path (absolute; testing/development)")
	flag.StringVar(&connectionPath, "connection", "", "explicit connection profile path (default: $OMATORRENT_CONNECTION or $XDG_CONFIG_HOME/omatorrent/connection.json)")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if err := run(log, configPath, socketOverride, connectionPath); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, configPath, socketOverride, connectionPath string) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if socketOverride != "" {
		cfg.IPC.SocketPath = socketOverride
	}

	// Connection profile (ADR-0008): the daemon-owned store wins; the
	// service.json endpoint is the fallback local profile. Credentials
	// come from the Secret Service only (ADR-0009) — config passwords
	// are already rejected at load.
	if connectionPath == "" {
		connectionPath = os.Getenv("OMATORRENT_CONNECTION")
	}
	if connectionPath == "" {
		if p, err := connection.DefaultStorePath(); err == nil {
			connectionPath = p
		}
	}
	fallback := connection.Profile{URL: cfg.QBittorrent.URL, Username: cfg.QBittorrent.Username, TLSMode: connection.TLSSystem}
	profile, _, err := connection.LoadActive(connectionPath, fallback)
	if err != nil {
		return err
	}
	prov := secrets.NewSecretTool()
	qbt, err := connection.BuildClient(profile, prov)
	if err != nil {
		return err
	}
	syncer := state.New(qbt, state.Options{}, log)
	mutator := mutate.New(qbt, syncer, mutate.Options{}, log)
	// The syncer is both the status source and the sync-side switcher;
	// the mutator guards the mutation side (ADR-0008 §8).
	manager, err := connection.NewManager(connectionPath, fallback, prov, syncer, syncer, mutator, log)
	if err != nil {
		return err
	}

	socketPath, err := ipc.ResolveSocketPath(cfg.IPC.SocketPath)
	if err != nil {
		return err
	}
	handler := &daemonHandler{syncer: syncer, mutator: mutator, manager: manager}
	srv, err := ipc.New(socketPath, handler, handler, handler, handler, log)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve() }()
	go syncer.Run(ctx)
	go mutator.Run(ctx)

	log.Info("omatorrent-service started",
		"version", version,
		"socket", socketPath,
		"qbittorrent_url", profile.URL, // endpoint only; never credentials
		"tls_mode", profile.TLSMode,
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

// daemonHandler adapts the state syncer and mutation orchestrator to
// the IPC interfaces: v1.0 status responses, v1.1 torrent
// subscriptions, v1.2 mutations.
type daemonHandler struct {
	syncer  *state.Syncer
	mutator *mutate.Mutator
	manager *connection.Manager
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

// Dashboard implements the v1.3 surface (ADR-0007): aggregates computed
// by the state layer from the committed snapshot. Degraded responses
// carry last-known counts/aggregate iff a cycle ever committed
// (Generation > 0); speeds/free-space/active are never fabricated.
func (h *daemonHandler) Dashboard() (ipc.DashboardData, bool) {
	st := h.syncer.State()
	agg := state.Aggregate(st)
	data := ipc.DashboardData{
		AppVersion:    st.AppVersion,
		WebAPIVersion: st.WebAPIVersion,
		DlSpeed:       st.DlSpeed,
		UpSpeed:       st.UpSpeed,
		FreeSpace:     agg.FreeSpace,
	}
	data.Counts = ipc.DashboardCounts{
		Total:       agg.Counts.Total,
		Active:      agg.Counts.Active,
		Downloading: agg.Counts.Downloading,
		Seeding:     agg.Counts.Seeding,
		Paused:      agg.Counts.Paused,
		Completed:   agg.Counts.Completed,
	}
	data.Aggregate = ipc.DashboardAggregate{
		TotalSize:      agg.Aggregate.TotalSize,
		CompletedBytes: agg.Aggregate.CompletedBytes,
		RemainingBytes: agg.Aggregate.RemainingBytes,
	}
	for _, it := range agg.Active {
		data.Active = append(data.Active, ipc.DashboardActiveItem{
			Name: it.Name, State: it.State, Progress: it.Progress,
			DlSpeed: it.DlSpeed, UpSpeed: it.UpSpeed,
		})
	}
	if st.BackendOK {
		return data, true
	}
	if st.Generation == 0 {
		// Never synced: no last-known state exists to show.
		return ipc.DashboardData{}, false
	}
	data.LastKnown = &ipc.DashboardLastKnownData{
		AppVersion:    st.AppVersion,
		WebAPIVersion: st.WebAPIVersion,
		Counts:        data.Counts,
		Aggregate:     data.Aggregate,
	}
	return data, false
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

// Submit implements ipc.Mutations: the mutator's Stage1 answer maps
// one-to-one onto the wire shape (accepted / rejection code / replayed
// terminal result).
func (h *daemonHandler) Submit(r ipc.MutationRequest) ipc.MutationStage1 {
	s1 := h.mutator.Submit(mutate.Request{
		Action:      mutate.Action(r.Action),
		Hash:        r.Hash,
		URL:         r.URL,
		DeleteFiles: r.DeleteFiles,
		Ref:         r.Ref,
	})
	out := ipc.MutationStage1{
		Outcome:  s1.Outcome,
		Mutation: s1.Mutation,
		Action:   string(s1.Action),
		Hash:     s1.Hash,
	}
	if s1.Replay != nil {
		out.Replay = &ipc.MutationResult{
			Mutation: s1.Replay.Mutation,
			Action:   string(s1.Replay.Action),
			Hash:     s1.Replay.Hash,
			Status:   s1.Replay.Status,
		}
	}
	return out
}

// Results implements ipc.Mutations: forwards the mutator's broadcast
// with bounded buffering (slow IPC consumers are dropped, consistent
// with the ADR-0005 slow-consumer rule).
func (h *daemonHandler) Results() (<-chan ipc.MutationResult, func()) {
	ch := make(chan ipc.MutationResult, 16)
	src, cancel := h.mutator.Results()
	go func() {
		defer close(ch)
		for r := range src {
			select {
			case ch <- ipc.MutationResult{Mutation: r.Mutation, Action: string(r.Action), Hash: r.Hash, Status: r.Status}:
			default:
				cancel()
				return
			}
		}
	}()
	return ch, cancel
}

// ---- v1.4 connection surface (ADR-0008): thin mapping between the
// ipc wire types and the connection Manager. No secret ever crosses
// back: status carries only has_secret, test/configure results carry
// only fixed codes and host labels. ----

func (h *daemonHandler) ConnectionStatus() ipc.ConnectionStatusData {
	st := h.manager.Status()
	return ipc.ConnectionStatusData{
		Configured: st.Configured,
		Mode:       st.Mode,
		URL:        st.URL,
		Host:       st.Host,
		Transport:  st.Transport,
		Insecure:   st.Insecure,
		Username:   st.Username,
		HasSecret:  st.HasSecret,
		TLSMode:    st.TLSMode,
		Pin:        st.Pin,
		Status:     st.Status,
		Detail:     st.Detail,
		Epoch:      st.Epoch,
	}
}

func (h *daemonHandler) ConnectionTest(r ipc.ConnectionTestRequest) ipc.ConnectionTestData {
	res := h.manager.Test(context.Background(), connection.TestParams{
		URL: r.URL, Username: r.Username, Password: r.Password,
		UseStoredPassword: r.UseStoredPassword, TLSMode: r.TLSMode,
		Pin: r.Pin, AllowInsecureHTTP: r.AllowInsecureHTTP,
	})
	return ipc.ConnectionTestData{
		OK: res.OK, Status: res.Status, Detail: res.Detail,
		Host: res.Host, Transport: res.Transport,
		AppVersion: res.AppVersion, WebAPIVersion: res.WebAPIVersion,
		OfferedFingerprint: res.OfferedFingerprint,
	}
}

func (h *daemonHandler) ConnectionConfigure(r ipc.ConnectionConfigureRequest) ipc.ConnectionConfigureData {
	res := h.manager.Configure(context.Background(), connection.ConfigureParams{
		TestParams: connection.TestParams{
			URL: r.URL, Username: r.Username, Password: r.Password,
			UseStoredPassword: r.UseStoredPassword, TLSMode: r.TLSMode,
			Pin: r.Pin, AllowInsecureHTTP: r.AllowInsecureHTTP,
		},
		SecretAction: r.SecretAction,
	})
	return ipc.ConnectionConfigureData{
		OK: res.OK, Rejection: res.Rejection, Epoch: res.Epoch,
		Mode: res.Mode, Host: res.Host, Transport: res.Transport,
	}
}
