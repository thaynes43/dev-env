package projectsync

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/thaynes43/dev-env/internal/agentd"
)

const receiptLimit = 256 << 10

type Result struct {
	Version                int                      `json:"version"`
	Workspace              string                   `json:"workspace"`
	JobUID                 string                   `json:"jobUID"`
	PodUID                 string                   `json:"podUID"`
	CatalogUID             string                   `json:"catalogUID"`
	CatalogResourceVersion string                   `json:"catalogResourceVersion"`
	CatalogRevision        string                   `json:"catalogRevision"`
	StartedAt              time.Time                `json:"startedAt"`
	Deadline               time.Time                `json:"deadline"`
	State                  string                   `json:"state"`
	Report                 agentd.ProjectSyncReport `json:"report"`
	Failure                string                   `json:"failure,omitempty"`
}

func privateDirectory(path string, create bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("sync private state path is invalid")
	}
	parent := filepath.Dir(path)
	if parent != path {
		if err := privateDirectory(parent, create); err != nil {
			return err
		}
	}
	fi, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		if !create {
			return nil
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			return errors.New("sync private state directory creation is unconfirmed")
		}
		dir, openErr := os.Open(parent)
		if openErr != nil {
			return errors.New("sync private state parent is unconfirmed")
		}
		syncErr := dir.Sync()
		closeErr := dir.Close()
		if syncErr != nil || closeErr != nil {
			return errors.New("sync private state directory durability is unconfirmed")
		}
		fi, err = os.Lstat(path)
	}
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return errors.New("sync private state path is not a plain directory")
	}
	return nil
}

func readReceipt(path string) (Result, error) {
	var result Result
	fi, err := os.Lstat(path)
	if err != nil {
		return result, err
	}
	if !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || fi.Size() > receiptLimit {
		return result, errors.New("sync operation receipt is unsafe")
	}
	f, err := os.Open(path)
	if err != nil {
		return result, err
	}
	defer func() { _ = f.Close() }()
	d := json.NewDecoder(io.LimitReader(f, receiptLimit+1))
	d.DisallowUnknownFields()
	if d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF || result.Version != 1 || result.Workspace == "" || result.JobUID == "" || result.PodUID == "" || result.CatalogUID == "" || result.CatalogResourceVersion == "" || result.CatalogRevision == "" || result.StartedAt.IsZero() || result.Deadline.IsZero() || !result.StartedAt.Before(result.Deadline) || result.State != "Started" && result.State != "Terminal" {
		return Result{}, errors.New("sync operation receipt is unconfirmed")
	}
	return result, nil
}

var writeReceipt = func(path string, result Result, exclusive bool) error {
	data, err := json.Marshal(result)
	if err != nil || len(data) > receiptLimit {
		return errors.New("sync operation result exceeds its private receipt bound")
	}
	data = append(data, '\n')
	var f *os.File
	if exclusive {
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_CREAT|syscall.O_EXCL|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
		if err != nil {
			return errors.New("sync operation has already started or its write is unconfirmed")
		}
		f = os.NewFile(uintptr(fd), path)
	} else {
		old, err := readReceipt(path)
		if err != nil || old.State != "Started" || old.JobUID != result.JobUID || old.PodUID != result.PodUID || old.Workspace != result.Workspace || old.CatalogUID != result.CatalogUID || old.CatalogResourceVersion != result.CatalogResourceVersion || old.CatalogRevision != result.CatalogRevision || !old.StartedAt.Equal(result.StartedAt) || !old.Deadline.Equal(result.Deadline) {
			return errors.New("sync operation changed before terminal receipt")
		}
		f, err = os.CreateTemp(filepath.Dir(path), ".result-")
		if err != nil {
			return errors.New("sync terminal result write is unconfirmed")
		}
		defer func() { _ = os.Remove(f.Name()) }()
		if f.Chmod(0o600) != nil {
			_ = f.Close()
			return errors.New("sync private result mode is unconfirmed")
		}
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(data); err != nil {
		return errors.New("sync operation receipt write is unconfirmed")
	}
	if f.Sync() != nil || f.Close() != nil {
		return errors.New("sync operation receipt durability is unconfirmed")
	}
	if !exclusive {
		if os.Rename(f.Name(), path) != nil {
			return errors.New("sync terminal result publication is unconfirmed")
		}
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return errors.New("sync receipt directory is unconfirmed")
	}
	defer func() { _ = dir.Close() }()
	if dir.Sync() != nil {
		return errors.New("sync receipt directory durability is unconfirmed")
	}
	confirmed, err := readReceipt(path)
	actual, marshalErr := json.Marshal(confirmed)
	if err != nil || marshalErr != nil || !bytes.Equal(actual, bytes.TrimSpace(data)) {
		return errors.New("sync operation receipt acknowledgment is unconfirmed")
	}
	return nil
}
