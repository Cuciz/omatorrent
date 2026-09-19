package ipc

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Limits (docs/IPC.md).
const (
	maxClients        = 16
	handshakeDeadline = 5 * time.Second
	idleReadDeadline  = 30 * time.Second
	writeDeadline     = 5 * time.Second
)

// Handler supplies backend state for health and system.status. It must be
// safe for concurrent use and must not block (served from cache).
type Handler interface {
	Health() bool
	StatusData() (StatusData, bool)
}

// Subscriptions supplies torrent state for v1.1 subscriptions
// (ADR-0005). Subscribe returns the committed full state and a channel
// of subsequent changes; cancel stops delivery. Implementations must
// close the channel when the subscriber is dropped.
type Subscriptions interface {
	Subscribe() (items []TorrentItem, events <-chan DeltaEvent, cancel func())
}

// Server is the IPC v1 Unix-socket server. Socket lifecycle follows
// ADR-0004: fail closed on unsafe paths, refuse any existing socket path,
// never auto-delete, remove on shutdown only by file identity.
type Server struct {
	socketPath string
	handler    Handler
	subs       Subscriptions
	log        *slog.Logger

	ln        net.Listener
	sockDev   uint64
	sockIno   uint64
	mu        sync.Mutex
	clients   map[net.Conn]struct{}
	closing   bool
	wg        sync.WaitGroup
	closeOnce sync.Once
}

// ResolveSocketPath computes and validates the socket path. explicit is
// used when non-empty (absolute path; its parent directory must exist,
// belong to the current UID and permit no group/world access). Otherwise
// the default $XDG_RUNTIME_DIR/omatorrent/service.sock is validated and
// its parent directory created 0700.
func ResolveSocketPath(explicit string) (string, error) {
	if explicit != "" {
		if !filepath.IsAbs(explicit) {
			return "", fmt.Errorf("ipc: explicit socket path must be absolute: %s", explicit)
		}
		dir := filepath.Dir(explicit)
		if err := validatePrivateDir(dir, true); err != nil {
			return "", err
		}
		return explicit, nil
	}

	xrd := os.Getenv("XDG_RUNTIME_DIR")
	if xrd == "" {
		return "", fmt.Errorf("ipc: XDG_RUNTIME_DIR is not set")
	}
	if !filepath.IsAbs(xrd) {
		return "", fmt.Errorf("ipc: XDG_RUNTIME_DIR must be absolute: %s", xrd)
	}
	if err := validatePrivateDir(xrd, true); err != nil {
		return "", err
	}
	appDir := filepath.Join(xrd, "omatorrent")
	if err := ensureAppDir(appDir); err != nil {
		return "", err
	}
	return filepath.Join(appDir, "service.sock"), nil
}

// validatePrivateDir checks that dir exists, is a directory owned by the
// current UID with no symlink at any component from / to dir, and (when
// exact is true) permits no group/world access.
func validatePrivateDir(dir string, exact bool) error {
	// Component-wise symlink refusal from root down.
	components := strings.Split(strings.TrimPrefix(dir, "/"), string(os.PathSeparator))
	cur := string(os.PathSeparator)
	for _, c := range components {
		if c == "" {
			continue
		}
		cur = filepath.Join(cur, c)
		if fi, err := os.Lstat(cur); err != nil {
			return fmt.Errorf("ipc: runtime path component %s: %w", cur, err)
		} else if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("ipc: runtime path component %s is a symlink", cur)
		}
	}

	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("ipc: runtime directory %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("ipc: runtime path %s is not a directory", dir)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("ipc: runtime directory %s is not owned by uid %d", dir, os.Getuid())
	}
	perm := fi.Mode().Perm()
	if perm&0o077 != 0 {
		return fmt.Errorf("ipc: runtime directory %s permits group/world access (mode %04o)", dir, perm)
	}
	if exact && perm&0o700 != 0o700 {
		return fmt.Errorf("ipc: runtime directory %s lacks owner rwx (mode %04o)", dir, perm)
	}
	return nil
}

// ensureAppDir creates the omatorrent subdirectory 0700, or validates an
// existing one. No permission repair, no symlink acceptance.
func ensureAppDir(dir string) error {
	fi, err := os.Lstat(dir)
	switch {
	case err == nil:
		if fi.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("ipc: refusing symlinked application directory %s", dir)
		}
		if !fi.IsDir() {
			return fmt.Errorf("ipc: refusing non-directory application path %s", dir)
		}
		if fi.Mode().Perm() != 0o700 {
			return fmt.Errorf("ipc: refusing existing application directory %s with mode %04o", dir, fi.Mode().Perm())
		}
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != uint32(os.Getuid()) {
			return fmt.Errorf("ipc: refusing application directory %s not owned by uid %d", dir, os.Getuid())
		}
		return nil
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(dir, 0o700); err != nil {
			return fmt.Errorf("ipc: create application directory %s: %w", dir, err)
		}
		return nil
	default:
		return fmt.Errorf("ipc: inspect application directory %s: %w", dir, err)
	}
}

// New validates the socket path and prepares the server. Call Serve to
// accept connections.
func New(socketPath string, handler Handler, subs Subscriptions, log *slog.Logger) (*Server, error) {
	if handler == nil {
		return nil, fmt.Errorf("ipc: nil handler")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		socketPath: socketPath,
		handler:    handler,
		subs:       subs,
		log:        log,
		clients:    make(map[net.Conn]struct{}),
	}, nil
}

// recoverStaleSocket decides what to do with an existing socket path
// before listening. Policy (fail closed on ambiguity):
//
//   - path absent: nothing to do.
//   - live daemon answers an IPC v1 hello on it: refuse startup.
//   - socket file of the exact expected shape (UID-owned, mode 0600,
//     type socket, no symlink) whose listener is PROVEN dead
//     (connect → ECONNREFUSED): remove it and proceed. Removal is
//     guarded by a file-identity re-check so a socket swapped between
//     probe and removal is not deleted.
//   - anything else (wrong type/owner/permissions, symlink, connect
//     timeout, protocol garbage, unexpected errors): refuse startup.
//
// Residual same-UID races remain inside the ADR-0004 trust boundary.
func (s *Server) recoverStaleSocket() error {
	fi, err := os.Lstat(s.socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("ipc: inspect socket path %s: %w", s.socketPath, err)
	}

	// Shape checks: exact type, owner and permissions.
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("ipc: refusing symlink at socket path %s", s.socketPath)
	}
	if fi.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("ipc: refusing non-socket file at socket path %s", s.socketPath)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); !ok || st.Uid != uint32(os.Getuid()) {
		return fmt.Errorf("ipc: refusing socket %s not owned by uid %d", s.socketPath, os.Getuid())
	}
	if fi.Mode().Perm() != 0o600 {
		return fmt.Errorf("ipc: refusing socket %s with mode %04o (want 0600)", s.socketPath, fi.Mode().Perm())
	}

	// Liveness probe. ECONNREFUSED is the only "stale" verdict; any
	// connection attempt that succeeds gets a full hello handshake — a
	// live omatorrent-service answers and startup is refused. Timeouts
	// or garbage are ambiguous → refuse. A connect ENOENT means the
	// socket vanished mid-probe: re-check once; still-present → refuse.
	live, err := probeLiveDaemon(s.socketPath)
	if err != nil {
		if isNotExistConnect(err) {
			if _, serr := os.Lstat(s.socketPath); errors.Is(serr, os.ErrNotExist) {
				return nil // legitimately gone; nothing to recover
			}
		}
		return fmt.Errorf("ipc: refusing ambiguous socket %s: %w", s.socketPath, err)
	}
	if live {
		return fmt.Errorf("ipc: refusing socket %s: another omatorrent-service is answering on it", s.socketPath)
	}

	// Stale proven. Re-check identity, then remove only what was probed.
	fi2, err := os.Lstat(s.socketPath)
	if err != nil {
		return fmt.Errorf("ipc: re-inspect stale socket %s: %w", s.socketPath, err)
	}
	if !sameFile(fi, fi2) {
		return fmt.Errorf("ipc: refusing socket %s: path changed during staleness probe", s.socketPath)
	}
	if err := os.Remove(s.socketPath); err != nil {
		return fmt.Errorf("ipc: remove stale socket %s: %w", s.socketPath, err)
	}
	s.log.Info("removed stale socket (listener proven dead, identity verified)", "path", s.socketPath)
	return nil
}

// probeLiveDaemon dials the socket and performs an IPC v1 hello.
// Returns (true, nil) when a daemon answers with the expected hello;
// (false, nil) only on ECONNREFUSED (no listener bound); any other
// outcome is an error (ambiguous). The answer read is byte-capped at
// MaxFrame so a garbage-streaming listener cannot grow memory.
func probeLiveDaemon(path string) (live bool, err error) {
	conn, err := net.DialTimeout("unix", path, 1500*time.Millisecond)
	if err != nil {
		if isConnectionRefused(err) {
			return false, nil
		}
		return false, fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(1500 * time.Millisecond))
	if _, err := conn.Write([]byte("{\"type\":\"hello\",\"protocol\":1}\n")); err != nil {
		return false, fmt.Errorf("probe write: %w", err)
	}
	capped := struct{ io.Reader }{io.LimitReader(conn, MaxFrame)}
	line, err := bufio.NewReaderSize(capped.Reader, MaxFrame).ReadString('\n')
	if err != nil {
		return false, fmt.Errorf("probe read: %w", err)
	}
	if strings.TrimSuffix(line, "\n") == string(EncodeHello()) {
		return true, nil
	}
	return false, fmt.Errorf("unexpected probe answer")
}

func isConnectionRefused(err error) bool {
	var oerr *net.OpError
	if errors.As(err, &oerr) {
		return errors.Is(oerr.Err, syscall.ECONNREFUSED)
	}
	return errors.Is(err, syscall.ECONNREFUSED)
}

// isNotExistConnect reports a connect failure caused by the socket path
// disappearing between Lstat and the dial.
func isNotExistConnect(err error) bool {
	var oerr *net.OpError
	if errors.As(err, &oerr) {
		return errors.Is(oerr.Err, os.ErrNotExist) || errors.Is(oerr.Err, syscall.ENOENT)
	}
	return errors.Is(err, os.ErrNotExist)
}

func sameFile(a, b os.FileInfo) bool {
	return a.Mode() == b.Mode() &&
		os.SameFile(a, b)
}

// Serve creates the listener and serves until Close. The socket is created
// with a restrictive umask and explicitly chmod 0600. A pre-existing path
// is refused unless it is a PROVEN-stale socket (see recoverStaleSocket);
// anything ambiguous fails closed. Go's automatic unlink-on-close is
// disabled; Close removes the socket only when its file identity still
// matches.
func (s *Server) Serve() error {
	if err := s.recoverStaleSocket(); err != nil {
		return err
	}

	oldMask := syscall.Umask(0o077)
	ln, err := net.Listen("unix", s.socketPath)
	syscall.Umask(oldMask)
	if err != nil {
		return fmt.Errorf("ipc: listen %s: %w", s.socketPath, err)
	}
	if ul, ok := ln.(*net.UnixListener); ok {
		ul.SetUnlinkOnClose(false)
	}
	if err := os.Chmod(s.socketPath, 0o600); err != nil {
		ln.Close()
		return fmt.Errorf("ipc: chmod socket %s: %w", s.socketPath, err)
	}
	fi, err := os.Stat(s.socketPath)
	if err != nil {
		ln.Close()
		return fmt.Errorf("ipc: stat socket %s: %w", s.socketPath, err)
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		s.sockDev, s.sockIno = uint64(st.Dev), uint64(st.Ino)
	}

	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closing := s.closing
			s.mu.Unlock()
			if closing {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return fmt.Errorf("ipc: accept: %w", err)
		}
		s.mu.Lock()
		if s.closing {
			s.mu.Unlock()
			conn.Close()
			continue
		}
		if len(s.clients) >= maxClients {
			s.mu.Unlock()
			conn.Close() // excess client: close without a response
			continue
		}
		s.clients[conn] = struct{}{}
		s.mu.Unlock()

		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handle(conn)
		}()
	}
}

// Close shuts down: closes the listener and every client connection,
// waits for handlers, then removes the socket only if its file identity
// still matches the one this process created. It is idempotent.
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closing = true
		ln := s.ln
		for c := range s.clients {
			c.Close()
		}
		s.mu.Unlock()
		if ln != nil {
			ln.Close()
		}
		s.wg.Wait()

		if s.sockIno != 0 {
			if fi, err := os.Stat(s.socketPath); err == nil {
				if st, ok := fi.Sys().(*syscall.Stat_t); ok &&
					uint64(st.Dev) == s.sockDev && uint64(st.Ino) == s.sockIno {
					if err := os.Remove(s.socketPath); err != nil {
						s.log.Error("ipc: remove socket on shutdown failed", "path", s.socketPath, "error", err)
					}
				} else {
					s.log.Warn("ipc: socket replaced by another instance; leaving it in place", "path", s.socketPath)
				}
			} else if errors.Is(err, os.ErrNotExist) {
				// Already gone; nothing to remove.
			} else {
				s.log.Error("ipc: stat socket on shutdown failed", "path", s.socketPath, "error", err)
			}
		}
	})
}

// handle runs one connection: handshake, then the request loop. All
// writes (responses and v1.1 pushes) are serialized through a bounded
// outbound queue drained by one writer goroutine; a full queue or a
// failed write tears the connection down (ADR-0005 slow-consumer rule).
func (s *Server) handle(conn net.Conn) {
	c := newConnIO(conn)
	defer func() {
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
		c.teardown()
	}()

	reader := bufio.NewReaderSize(conn, MaxFrame)

	// Handshake (direct write; queue not started yet).
	conn.SetDeadline(time.Now().Add(handshakeDeadline))
	req, err := readRequest(reader)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return // clean close before any frame
		}
		s.fail(conn, err)
		return
	}
	if req.Type != "hello" {
		// Schema was valid (parse succeeded); wrong first message.
		s.fail(conn, errHandshake)
		return
	}
	if req.Protocol != ProtocolVersion {
		s.fail(conn, errVersion)
		return
	}
	if err := writeFrame(conn, EncodeHello()); err != nil {
		return
	}
	conn.SetDeadline(time.Time{}) // clear; loop sets read deadlines

	// Writer goroutine owns the socket from here on.
	c.sendMu.Lock()
	c.writerStarted = true
	c.sendMu.Unlock()
	go c.writer()

	for {
		conn.SetReadDeadline(time.Now().Add(idleReadDeadline))
		req, err := readRequest(reader)
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return // clean disconnect
			}
			if errors.Is(err, os.ErrDeadlineExceeded) {
				return // idle timeout: close without error frame
			}
			c.send(EncodeError(failCode(err)))
			return
		}

		switch req.Type {
		case "hello":
			c.send(EncodeError(failCode(errUnsupported)))
			return
		case "health":
			c.send(EncodeHealth(req.ID, s.handler.Health()))
		case "system.status":
			data, ok := s.handler.StatusData()
			c.send(EncodeStatus(req.ID, ok, data))
		case "torrent.subscribe":
			if s.subs == nil {
				c.send(EncodeError(failCode(errUnsupported)))
				return
			}
			if c.markSubscribed() {
				s.startSubscription(c, req.ID) // delivers subscribed + snapshot with backpressure
			} else {
				// One subscription per connection; the existing one keeps
				// working and the connection stays open (documented in
				// docs/IPC.md v1.1 lifecycle).
				c.send(EncodeError(failCode(errUnsupported)))
			}
		default:
			c.send(EncodeError(failCode(errUnsupported)))
			return
		}
		if !c.alive() {
			return
		}
	}
}

// startSubscription delivers the full snapshot with BACKPRESSURE and
// then hands the connection to the delta pump (ADR-0005 flow control,
// review finding 6): snapshot frames wait for queue space (bounded
// memory, single writer, no healthy-subscriber disconnect merely
// because the snapshot is larger than the delta queue), while live
// deltas use the bounded non-blocking queue with disconnect-on-overflow.
// A change committed during snapshot delivery waits in the events
// channel and is delivered strictly after snapshot.end (subscription
// registration is atomic with the snapshot's committed generation).
func (s *Server) startSubscription(c *connIO, id int64) {
	items, events, cancel := s.subs.Subscribe()
	c.setSubCancel(cancel)

	sortItems(items)

	deadline := time.Now().Add(snapshotDeliveryTimeout)
	abort := func() {
		cancel()
		c.teardown()
	}
	if !c.sendBlocking(EncodeSubscribed(id), deadline) {
		abort()
		return
	}
	if !c.sendBlocking(EncodeSnapshotBegin(id, len(items)), deadline) {
		abort()
		return
	}
	for i := range items {
		frame := EncodeSnapshotItem(id, i, items[i])
		if frame == nil {
			// Cannot be framed: abort rather than silently omit.
			abort()
			return
		}
		if !c.sendBlocking(frame, deadline) {
			abort()
			return
		}
	}
	if !c.sendBlocking(EncodeSnapshotEnd(id), deadline) {
		abort()
		return
	}

	go func() {
		for ev := range events {
			frames, ok := EncodeDeltas(ev)
			if !ok {
				// A committed update cannot be framed — terminate the
				// subscriber so it reconnects and rebuilds (no silent
				// loss).
				cancel()
				c.teardown()
				return
			}
			for _, frame := range frames {
				if !c.send(frame) {
					cancel()
					return
				}
			}
		}
		// events closed: the state source dropped us; the client must
		// rebuild on a fresh connection.
		c.teardown()
	}()
}

// sortItems gives the snapshot a deterministic wire order (name, hash).
func sortItems(items []TorrentItem) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].Name != items[j].Name {
			return items[i].Name < items[j].Name
		}
		return items[i].Hash < items[j].Hash
	})
}

// fail sends the mapped error frame directly (pre-queue, handshake
// phase only); payload is never echoed.
func (s *Server) fail(conn net.Conn, err error) {
	writeFrame(conn, EncodeError(failCode(err)))
}

// connIO serializes writes for one connection with a bounded queue.
type connIO struct {
	conn net.Conn
	out  chan []byte

	sendMu        sync.Mutex
	closed        bool
	writerStarted bool
	subMu         sync.Mutex
	subCancel     func()
	subscribed    bool
}

const outQueue = 256 // frames per connection for LIVE deltas (ADR-0005)

// snapshotDeliveryTimeout bounds the total backpressured delivery of
// one full snapshot; a subscriber that cannot drain it in this window
// is disconnected (slow-consumer rule).
var snapshotDeliveryTimeout = 30 * time.Second

func newConnIO(conn net.Conn) *connIO {
	return &connIO{conn: conn, out: make(chan []byte, outQueue)}
}

func (c *connIO) alive() bool {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	return !c.closed
}

// send enqueues one frame (without LF); false means the connection is
// gone or the consumer is too slow and was disconnected.
func (c *connIO) send(frame []byte) bool {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	if c.closed {
		return false
	}
	f := append(frame, '\n')
	select {
	case c.out <- f:
		return true
	default:
		c.closed = true
		close(c.out)
		c.cancelSubLocked()
		if !c.writerStarted {
			c.conn.Close()
		}
		return false
	}
}

// sendBlocking enqueues one frame, WAITING for queue space until the
// deadline (snapshot backpressure): bounded memory without
// disconnecting a healthy subscriber whose snapshot simply exceeds the
// live-delta queue size. false means timeout/teardown.
func (c *connIO) sendBlocking(frame []byte, deadline time.Time) bool {
	f := append(frame, '\n')
	for {
		c.sendMu.Lock()
		if c.closed {
			c.sendMu.Unlock()
			return false
		}
		select {
		case c.out <- f:
			c.sendMu.Unlock()
			return true
		default:
		}
		c.sendMu.Unlock()
		if time.Now().After(deadline) {
			c.teardown()
			return false
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// teardown stops the queue and closes the connection (idempotent).
// Frames already enqueued are still drained by the writer before it
// closes the socket, so terminal error frames reach the client.
func (c *connIO) teardown() {
	c.sendMu.Lock()
	if c.closed {
		c.sendMu.Unlock()
		return
	}
	c.closed = true
	started := c.writerStarted
	close(c.out)
	c.sendMu.Unlock()
	c.cancelSubLocked()
	if !started {
		c.conn.Close() // no writer running; close directly
	}
}

func (c *connIO) setSubCancel(cancel func()) {
	c.subMu.Lock()
	c.subCancel = cancel
	c.subMu.Unlock()
}

// markSubscribed latches the one-subscription-per-connection rule
// atomically (read loop is single-threaded, but keep it structural).
func (c *connIO) markSubscribed() bool {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	if c.subscribed {
		return false
	}
	c.subscribed = true
	return true
}

func (c *connIO) cancelSubLocked() {
	c.subMu.Lock()
	fn := c.subCancel
	c.subCancel = nil
	c.subMu.Unlock()
	if fn != nil {
		fn()
	}
}

// writer drains the outbound queue to the socket, then closes it. A
// failed write tears the connection down immediately so a blocked
// sendBlocking (snapshot backpressure) unblocks instead of spinning to
// its deadline.
func (c *connIO) writer() {
	for f := range c.out {
		c.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
		if _, err := c.conn.Write(f); err != nil {
			c.teardown()
			return
		}
	}
	c.conn.Close()
}

// readRequest reads one LF-terminated frame and parses it. Errors are
// classified sentinels: errTooLarge, errInvalid (covers parse/schema),
// io.EOF (clean close between frames).
func readRequest(r *bufio.Reader) (Request, error) {
	frame := make([]byte, 0, 256)
	for {
		slice, err := r.ReadSlice('\n')
		frame = append(frame, slice...)
		if errors.Is(err, bufio.ErrBufferFull) {
			if len(frame) > MaxFrame {
				return Request{}, errTooLarge
			}
			continue
		}
		if err != nil {
			if len(frame) == 0 {
				return Request{}, io.EOF // clean close between frames
			}
			return Request{}, errInvalid // incomplete frame on EOF/error
		}
		break
	}
	if len(frame) > MaxFrame {
		return Request{}, errTooLarge
	}
	frame = frame[:len(frame)-1] // strip LF

	req, perr := parseFrame(frame)
	if perr != nil {
		return Request{}, errInvalid
	}
	return req, nil
}

// writeFrame writes one response frame with the 5s write deadline.
func writeFrame(conn net.Conn, payload []byte) error {
	conn.SetWriteDeadline(time.Now().Add(writeDeadline))
	if _, err := conn.Write(append(payload, '\n')); err != nil {
		return err
	}
	return nil
}
