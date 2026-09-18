// ot-probe is a minimal IPC v1 client used for validation and debugging:
// it performs the handshake, then requests health and system.status and
// prints the raw response frames. It holds no secrets and only speaks the
// documented protocol.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

func main() {
	socket := flag.String("socket", "", "socket path (default $XDG_RUNTIME_DIR/omatorrent/service.sock)")
	interval := flag.Duration("interval", 2*time.Second, "delay between system.status requests")
	count := flag.Int("count", 1, "number of system.status requests")
	flag.Parse()

	path := *socket
	if path == "" {
		xrd := os.Getenv("XDG_RUNTIME_DIR")
		if xrd == "" {
			fmt.Fprintln(os.Stderr, "ot-probe: XDG_RUNTIME_DIR not set")
			os.Exit(1)
		}
		path = filepath.Join(xrd, "omatorrent", "service.sock")
	}

	conn, err := net.Dial("unix", path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ot-probe: dial %s: %v\n", path, err)
		os.Exit(1)
	}
	defer conn.Close()
	r := bufio.NewReader(conn)

	send := func(line string) string {
		conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte(line + "\n")); err != nil {
			fmt.Fprintf(os.Stderr, "ot-probe: write: %v\n", err)
			os.Exit(1)
		}
		resp, err := r.ReadString('\n')
		if err != nil {
			fmt.Fprintf(os.Stderr, "ot-probe: read: %v\n", err)
			os.Exit(1)
		}
		return resp
	}

	fmt.Print(send(`{"type":"hello","protocol":1}`))
	fmt.Print(send(`{"type":"health","id":1}`))
	for i := 1; i <= *count; i++ {
		fmt.Print(send(fmt.Sprintf(`{"type":"system.status","id":%d}`, i+1)))
		if i < *count {
			time.Sleep(*interval)
		}
	}
}
