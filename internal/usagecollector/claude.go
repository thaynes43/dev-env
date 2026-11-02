// Package usagecollector owns explicitly invoked local supported-source capture.
// It does not install settings, start a provider or enable recurring publication.
package usagecollector

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

const ClaudeFile = "claude-captures.json"
const maxJournalBytes = 16 << 10

var ErrCapture = errors.New("provider capture unavailable")
var token = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,127}$`)

type entry struct {
	Provider            string          `json:"provider"`
	ProducerRef         string          `json:"producerRef"`
	ProducerIncarnation string          `json:"producerIncarnation"`
	Sequence            int64           `json:"sequence"`
	CapturedAt          string          `json:"capturedAt"`
	ReceivedAt          string          `json:"receivedAt"`
	Payload             json.RawMessage `json:"payload"`
}
type journal struct {
	Schema   string  `json:"schema"`
	Captures []entry `json:"captures"`
}

// RecordClaude records one callback in an already-created private directory.
// It retains at most two sanitized captures and never rewrites invalid state.
func RecordClaude(ctx context.Context, dir, producer, incarnation string, raw []byte, now time.Time) error {
	if ctx == nil || ctx.Err() != nil || now.IsZero() || !token.MatchString(producer) || !token.MatchString(incarnation) {
		return ErrCapture
	}
	payload, err := providerallowance.SanitizeClaudeStatusline(raw)
	if err != nil {
		return ErrCapture
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return ErrCapture
	}
	defer func() { _ = root.Close() }()
	d, err := root.Open(".")
	if err != nil {
		return ErrCapture
	}
	info, err := d.Stat()
	_ = d.Close()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !owned(info) {
		return ErrCapture
	}
	lock, err := root.OpenFile("claude-captures.lock", os.O_CREATE|os.O_RDWR|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return ErrCapture
	}
	defer func() { _ = lock.Close() }()
	if !privateFile(lock) {
		return ErrCapture
	}
	lockCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	for {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			return ErrCapture
		}
		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-lockCtx.Done():
			timer.Stop()
			return ErrCapture
		case <-timer.C:
		}
	}
	defer func() { _ = unix.Flock(int(lock.Fd()), unix.LOCK_UN) }()
	j := journal{Schema: providerallowance.CaptureBundleSchema, Captures: []entry{}}
	f, err := root.OpenFile(ClaudeFile, os.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err == nil {
		if !privateFile(f) {
			_ = f.Close()
			return ErrCapture
		}
		body, e := io.ReadAll(io.LimitReader(f, maxJournalBytes+1))
		_ = f.Close()
		if e != nil || len(body) > maxJournalBytes || !decodeJournal(body, &j, producer, incarnation, now) {
			return ErrCapture
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrCapture
	}
	seq := int64(1)
	at := now.UTC().Format(time.RFC3339Nano)
	if len(j.Captures) > 0 {
		last := j.Captures[len(j.Captures)-1]
		prior, _ := time.Parse(time.RFC3339Nano, last.CapturedAt)
		if !now.After(prior) || last.Sequence >= 1_000_000 {
			return ErrCapture
		}
		seq = last.Sequence + 1
	}
	j.Captures = append(j.Captures, entry{Provider: "anthropic", ProducerRef: producer, ProducerIncarnation: incarnation, Sequence: seq, CapturedAt: at, ReceivedAt: at, Payload: payload})
	if len(j.Captures) > 2 {
		j.Captures = j.Captures[len(j.Captures)-2:]
	}
	body, err := json.Marshal(j)
	if err != nil || len(body) > maxJournalBytes || ctx.Err() != nil {
		return ErrCapture
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return ErrCapture
	}
	name := ".claude-capture-" + hex.EncodeToString(nonce[:])
	tmp, err := root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY|unix.O_NOFOLLOW, 0600)
	if err != nil {
		return ErrCapture
	}
	defer func() { _ = root.Remove(name) }()
	n, writeErr := tmp.Write(body)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if writeErr != nil || n != len(body) || syncErr != nil || closeErr != nil || ctx.Err() != nil {
		return ErrCapture
	}
	if root.Rename(name, ClaudeFile) != nil {
		return ErrCapture
	}
	directory, err := root.Open(".")
	if err != nil {
		return ErrCapture
	}
	err = directory.Sync()
	_ = directory.Close()
	if err != nil {
		return ErrCapture
	}
	return nil
}

func owned(info os.FileInfo) bool {
	s, ok := info.Sys().(*syscall.Stat_t)
	return ok && s.Uid == uint32(os.Geteuid())
}
func privateFile(f *os.File) bool {
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !owned(info) {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
func decodeJournal(body []byte, j *journal, producer, incarnation string, now time.Time) bool {
	// The existing closed assembler checks duplicate keys, bounds, order,
	// sequences, timestamps, sanitization and the payload contract as well.
	trust := providerallowance.AggregateTrust{MaxAge: 1800, Registry: providerallowance.NewStaticRegistry(providerallowance.RegistryParts{})}
	if _, err := providerallowance.AssembleCaptureBundle(body, trust, now); err != nil {
		return false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if dec.Decode(j) != nil || len(j.Captures) == 0 || len(j.Captures) > 2 {
		return false
	}
	for _, e := range j.Captures {
		if e.Provider != "anthropic" || e.ProducerRef != producer || e.ProducerIncarnation != incarnation {
			return false
		}
	}
	return true
}
