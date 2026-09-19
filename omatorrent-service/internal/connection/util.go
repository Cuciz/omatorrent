// Small encoding/file helpers for the connection package.
package connection

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
)

// maxCAPEMBytes bounds a CA bundle read from disk.
const maxCAPEMBytes = 256 << 10

// pemEncode wraps a DER certificate as PEM.
func pemEncode(der []byte) []byte {
	var buf bytes.Buffer
	buf.WriteString("-----BEGIN CERTIFICATE-----\n")
	b64 := base64.StdEncoding.EncodeToString(der)
	for len(b64) > 64 {
		buf.WriteString(b64[:64])
		buf.WriteByte('\n')
		b64 = b64[64:]
	}
	buf.WriteString(b64)
	buf.WriteByte('\n')
	buf.WriteString("-----END CERTIFICATE-----\n")
	return buf.Bytes()
}

// readCA loads a PEM CA bundle for TLS `ca` mode. The path is
// user-configured (file-only mode, ADR-0008): regular file, no
// symlink, bounded size, fail closed.
func readCA(path string) ([]byte, error) {
	if path == "" {
		return nil, errors.New("connection: empty CA path")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("connection: refusing symlinked CA bundle %s", path)
		}
		return nil, fmt.Errorf("connection: open CA bundle %s: %w", path, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("connection: stat CA bundle %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("connection: CA bundle %s is not a regular file", path)
	}
	pem, err := io.ReadAll(io.LimitReader(f, maxCAPEMBytes+1))
	if err != nil {
		return nil, fmt.Errorf("connection: read CA bundle %s: %w", path, err)
	}
	if len(pem) > maxCAPEMBytes {
		return nil, fmt.Errorf("connection: CA bundle %s exceeds %d bytes", path, maxCAPEMBytes)
	}
	return pem, nil
}

// removeStore deletes the profile file (rollback to fallback). Symlink
// safety: remove() never follows; removing a symlinked path would only
// unlink the symlink, which cannot occur because SaveStore renames a
// regular file over the target.
func removeStore(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("connection: remove %s: %w", path, err)
	}
	return nil
}
