package main

import (
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/allowancecollector"
)

func parseAllowanceCollector(enabled bool, path string, sel *aggregateAllowanceSelection, leader bool) (*allowancecollector.Config, error) {
	if !enabled {
		if path != "" {
			return nil, allowancecollector.ErrCollector
		}
		return nil, nil
	}
	if sel == nil || !leader || !filepath.IsAbs(path) {
		return nil, allowancecollector.ErrCollector
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, allowancecollector.ErrCollector
	}
	f := os.NewFile(uintptr(fd), "allowance-collector-config")
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0007 != 0 || info.Size() > 8<<10 {
		return nil, allowancecollector.ErrCollector
	}
	raw, err := io.ReadAll(io.LimitReader(f, (8<<10)+1))
	if err != nil || len(raw) > 8<<10 {
		return nil, allowancecollector.ErrCollector
	}
	return allowancecollector.ParseConfig(raw, sel.trust, string(sel.publisherUID), sel.publisherSA, time.Now())
}
