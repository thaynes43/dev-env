//go:build linux

package usagecollector

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/thaynes43/dev-env/internal/providerallowance"
)

// RunInternal is the model-free streamed helper entrypoint. It accepts one
// bounded fixed request on stdin and emits only a sanitized machine receipt.
// The caller's main supplies an independent hard process deadline as well.
func RunInternal(ctx context.Context, in *os.File, out io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 105*time.Second)
	defer cancel()
	finished, watcher := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watcher)
		select {
		case <-ctx.Done():
			_ = in.Close()
		case <-finished:
		}
	}()
	defer func() { close(finished); <-watcher }()
	raw, err := io.ReadAll(io.LimitReader(in, (16<<10)+1))
	if err != nil || ctx.Err() != nil {
		return ErrCapture
	}
	request, err := ParseRemoteRequest(raw, time.Now())
	if err != nil {
		return ErrCapture
	}
	if os.Geteuid() != 1000 || os.Getenv("HOME") != "/home/dev" || os.Getenv("XDG_RUNTIME_DIR") != "/dev/shm/run-1000" || os.Getenv("DEV_ENV_POD_UID") != request.PodUID || os.Getenv("AGENTD_SESSION_FILE") != "" {
		return ErrCapture
	}
	// Select only public identity from the normal pod input. Do not print the
	// original document, prompt, environment or native conversation history.
	document := []byte(os.Getenv("AGENTD_SESSION"))
	if !providerallowance.ValidJSONDocument(document, 128<<10) || !providerallowance.CanonicalFieldNames(document, "name", "sessionUID", "mode", "agent", "restore", "workspace") {
		return ErrCapture
	}
	var owner struct {
		Name      string          `json:"name"`
		UID       string          `json:"sessionUID"`
		Mode      string          `json:"mode"`
		Agent     string          `json:"agent"`
		Restore   string          `json:"restore"`
		Workspace json.RawMessage `json:"workspace"`
	}
	if json.Unmarshal(document, &owner) != nil || owner.Name != request.RootName || owner.UID != request.SessionUID || owner.Mode != "remote" || owner.Agent != "claude" || owner.Restore != "" || len(owner.Workspace) != 0 {
		return ErrCapture
	}
	executable, err := os.Open("/proc/self/exe")
	if err != nil {
		return ErrCapture
	}
	hash := sha256.New()
	n, copyErr := io.Copy(hash, io.LimitReader(executable, (32<<20)+1))
	_ = executable.Close()
	if copyErr != nil || n > 32<<20 || hex.EncodeToString(hash.Sum(nil)) != request.HelperSHA {
		return ErrCapture
	}
	receipt, err := RunRemote(ctx, request, filepath.Join(os.Getenv("XDG_RUNTIME_DIR"), "dev-env-allowance-capture"), acquireBatch)
	if err != nil {
		return ErrCapture
	}
	if receipt.State == "busy" {
		_, err = io.WriteString(out, "{\"version\":1,\"state\":\"busy\"}\n")
		return err
	}
	// Completion has already been fsynced under the lock before this write.
	return json.NewEncoder(out).Encode(receipt)
}
