// Package tokenfile bounds reads of rotating projected bearer-token files.
package tokenfile

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"syscall"
)

// MaxBytes is the existing named-API token bound. Callers may use a smaller bound.
const MaxBytes int64 = 64 << 10

// Read follows projected-volume symlinks on every call, then verifies the opened
// descriptor before reading. Nonblocking open refuses a FIFO without waiting for
// a writer. The descriptor remains tied to the selected regular file if kubelet
// rotates the projection during the read. Errors contain no path or token bytes.
func Read(path string, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 || maxBytes > MaxBytes {
		return nil, errors.New("token file bound invalid")
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fs.ErrNotExist
		}
		return nil, errors.New("token file unavailable")
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() < 0 || st.Size() > maxBytes {
		return nil, errors.New("token file type or size invalid")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, errors.New("token file read unavailable or exceeds bound")
	}
	return data, nil
}
