package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

const managedOutputChunks = 4

var errManagedOutput = errors.New("managed output identity, private receipt or acknowledgement is unconfirmed")

type managedOutputInvocation struct {
	Invocation   string `json:"invocation"`
	SessionUID   string `json:"sessionUID"`
	PodUID       string `json:"podUID"`
	Provider     string `json:"provider"`
	LaunchSHA256 string `json:"launchSHA256"`
}
type managedOutputChunk struct {
	Ordinal int32  `json:"ordinal"`
	Key     string `json:"key"`
	SHA256  string `json:"sha256"`
	Bytes   int    `json:"bytes"`
}
type managedOutputIntent struct {
	managedOutputInvocation
	ConversationID string               `json:"conversationID"`
	Completeness   string               `json:"completeness"`
	Chunks         []managedOutputChunk `json:"chunks"`
}
type managedCapture struct {
	dir          string
	invocation   managedOutputInvocation
	conversation string
	confirmed    bool
	unknown      bool
	chunks       []string
}

func ownedCaptureDir(path string) error {
	if noSymlinkComponents(path) != nil {
		return errManagedOutput
	}
	fi, err := os.Lstat(path)
	if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		return errManagedOutput
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) {
		return errManagedOutput
	}
	return nil
}

func ownedCaptureRead(dir, name string, bound int) ([]byte, error) {
	if ownedCaptureDir(dir) != nil || name != filepath.Base(name) {
		return nil, errManagedOutput
	}
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, errManagedOutput
	}
	defer func() { _ = f.Close() }()
	before, err := f.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Mode().Perm() != 0o600 || before.Size() < 1 || before.Size() > int64(bound) {
		return nil, errManagedOutput
	}
	st, ok := before.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		return nil, errManagedOutput
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(bound)+1))
	after, statErr := f.Stat()
	if err != nil || statErr != nil || len(b) > bound || !os.SameFile(before, after) || before.Size() != after.Size() || before.Mode() != after.Mode() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errManagedOutput
	}
	post, ok := after.Sys().(*syscall.Stat_t)
	if !ok || post.Uid != st.Uid || post.Nlink != 1 || post.Ctim != st.Ctim {
		return nil, errManagedOutput
	}
	return b, nil
}

func captureJSON(dir, name string, value any) error {
	if ownedCaptureDir(dir) != nil {
		return errManagedOutput
	}
	b, err := json.Marshal(value)
	if err != nil || len(b) > 8192 || projectSyncExclusiveFile(filepath.Join(dir, name), b) != nil {
		return errManagedOutput
	}
	got, err := ownedCaptureRead(dir, name, 8192)
	if err != nil || !bytes.Equal(got, b) {
		return errManagedOutput
	}
	return nil
}

// A new directory and receipt are the single causal permission to invoke. Even
// an identical existing receipt is retained and refused, never reconstructed.
func beginManagedCapture(l Launch, stateDir string) (*managedCapture, error) {
	if !l.CaptureOutput {
		return nil, nil
	}
	if l.TUI || l.Resume || l.WorkspaceOwner != nil || !apiv1.ValidContextUID(l.SessionUID) || !apiv1.ValidContextUID(l.PodUID) || (l.Provider != protocol.AgentClaude && l.Provider != protocol.AgentCodex) || l.CaptureModel == "" || len(l.CaptureModel) > 256 || len(l.Argv) == 0 || noSymlinkComponents(stateDir) != nil {
		return nil, errManagedOutput
	}
	root := filepath.Join(stateDir, "managed-output")
	if err := os.Mkdir(root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, errManagedOutput
	}
	if ownedCaptureDir(root) != nil {
		return nil, errManagedOutput
	}
	id := apiv1.ManagedInvocation(l.SessionUID, l.PodUID)
	dir := filepath.Join(root, id)
	if os.Mkdir(dir, 0o700) != nil || ownedCaptureDir(dir) != nil {
		return nil, errManagedOutput
	}
	launch, err := json.Marshal(l)
	if err != nil {
		return nil, errManagedOutput
	}
	c := &managedCapture{dir: dir, conversation: l.ConversationID, invocation: managedOutputInvocation{Invocation: id, SessionUID: l.SessionUID, PodUID: l.PodUID, Provider: l.Provider, LaunchSHA256: apiv1.ContextHash(string(launch))}}
	if captureJSON(dir, "invocation.json", c.invocation) != nil {
		return nil, errManagedOutput
	}
	parent, err := os.Open(root)
	if err != nil {
		return nil, errManagedOutput
	}
	defer func() { _ = parent.Close() }()
	if parent.Sync() != nil {
		return nil, errManagedOutput
	}
	return c, nil
}

func (c *managedCapture) text(text string) {
	if c == nil {
		return
	}
	if !utf8.ValidString(text) {
		c.unknown = true
		return
	}
	for len(text) > 0 {
		if len(c.chunks) == 0 || len(c.chunks[len(c.chunks)-1]) == apiv1.MaxContextBytes {
			if len(c.chunks) == managedOutputChunks {
				c.unknown = true
				return
			}
			c.chunks = append(c.chunks, "")
		}
		i := len(c.chunks) - 1
		n := min(len(text), apiv1.MaxContextBytes-len(c.chunks[i]))
		for n > 0 && !utf8.ValidString(text[:n]) {
			n--
		}
		if n == 0 {
			if len(c.chunks) == managedOutputChunks {
				c.unknown = true
				return
			}
			c.chunks = append(c.chunks, "")
			continue
		}
		c.chunks[i] += text[:n]
		text = text[n:]
	}
}

func (c *managedCapture) claude(line []byte) {
	if c == nil {
		return
	}
	var e streamEvent
	if !utf8.Valid(line) || json.Unmarshal(line, &e) != nil || e.Type == "" {
		c.unknown = true
		return
	}
	if e.Type == "system" && e.Subtype == "init" {
		if e.SessionID != c.conversation || !apiv1.ValidManagedOutput(&apiv1.ManagedOutputAssertion{Invocation: c.invocation.Invocation, ConversationID: e.SessionID}) {
			c.unknown = true
			return
		}
		c.confirmed = true
	}
	if e.Type != "assistant" {
		return
	}
	if !c.confirmed || e.Message == nil {
		c.unknown = true
		return
	}
	var blocks []contentBlock
	if json.Unmarshal(e.Message.Content, &blocks) != nil {
		c.unknown = true
		return
	}
	for _, b := range blocks {
		if b.Type == "text" {
			c.text(b.Text)
		}
	}
}

func (c *managedCapture) retain(l Launch, complete bool) (managedOutputIntent, error) {
	var intent managedOutputIntent
	if c == nil {
		return intent, nil
	}
	got, err := ownedCaptureRead(c.dir, "invocation.json", 8192)
	want, _ := json.Marshal(c.invocation)
	if err != nil || !bytes.Equal(got, want) || l.SessionUID != c.invocation.SessionUID || l.PodUID != c.invocation.PodUID {
		return intent, errManagedOutput
	}
	intent = managedOutputIntent{managedOutputInvocation: c.invocation, ConversationID: l.ConversationID, Completeness: "PartialManagedOutput"}
	if !complete || c.unknown || !c.confirmed || !apiv1.ValidManagedOutput(&apiv1.ManagedOutputAssertion{Invocation: c.invocation.Invocation, ConversationID: l.ConversationID}) {
		intent.Completeness = "Unknown"
	}
	for i, text := range c.chunks {
		if text == "" {
			continue
		}
		name := apiv1.ManagedOutputKey(c.invocation.Invocation, int32(i))
		if projectSyncExclusiveFile(filepath.Join(c.dir, name+".txt"), []byte(text)) != nil {
			return intent, errManagedOutput
		}
		b, err := ownedCaptureRead(c.dir, name+".txt", apiv1.MaxContextBytes)
		if err != nil || string(b) != text {
			return intent, errManagedOutput
		}
		intent.Chunks = append(intent.Chunks, managedOutputChunk{Ordinal: int32(i), Key: name, SHA256: apiv1.ContextHash(text), Bytes: len(text)})
	}
	// The immutable outcome and original content intent precede every HTTP call.
	if captureJSON(c.dir, "publication-intent.json", intent) != nil {
		return intent, errManagedOutput
	}
	return intent, nil
}

func (c *managedCapture) publish(ctx context.Context, l Launch, intent managedOutputIntent, h *HeartbeatClient) error {
	if c == nil || h == nil || h.Session != l.Session || !apiv1.ValidManagedOutput(&apiv1.ManagedOutputAssertion{Invocation: intent.Invocation, ConversationID: intent.ConversationID}) {
		return errManagedOutput
	}
	saved, err := ownedCaptureRead(c.dir, "publication-intent.json", 8192)
	want, _ := json.Marshal(intent)
	if err != nil || !bytes.Equal(saved, want) {
		return errManagedOutput
	}
	if captureJSON(c.dir, "delivery-attempt.json", struct {
		State      string `json:"state"`
		Invocation string `json:"invocation"`
	}{"Unknown", intent.Invocation}) != nil {
		return errManagedOutput
	}
	for _, chunk := range intent.Chunks {
		b, err := ownedCaptureRead(c.dir, chunk.Key+".txt", apiv1.MaxContextBytes)
		if err != nil || len(b) != chunk.Bytes || !utf8.Valid(b) || apiv1.ContextHash(string(b)) != chunk.SHA256 {
			return errManagedOutput
		}
	}
	for _, chunk := range intent.Chunks {
		b, err := ownedCaptureRead(c.dir, chunk.Key+".txt", apiv1.MaxContextBytes)
		if err != nil || apiv1.ContextHash(string(b)) != chunk.SHA256 {
			return errManagedOutput
		}
		r := apiv1.PublishContextRequest{Kind: "transcript-excerpt", Key: chunk.Key, Text: string(b), ManagedOutput: &apiv1.ManagedOutputRequest{Ordinal: chunk.Ordinal, ConversationID: intent.ConversationID, Completeness: intent.Completeness}}
		var result apiv1.PublishContextResult
		if err := h.contextRequest(ctx, http.MethodPost, apiv1.PublishedContextsPath, r, &result); errors.Is(err, ErrContextLimit) {
			return ErrContextLimit
		} else if err != nil {
			return errManagedOutput
		}
		m := result.Metadata
		expected := &apiv1.ManagedOutputAssertion{Invocation: intent.Invocation, Ordinal: chunk.Ordinal, ConversationID: intent.ConversationID}
		if (result.Outcome != "Created" && result.Outcome != "Confirmed") || !validContextMetadata(m) || m.Source.Name != l.Session || m.Source.UID != l.SessionUID || m.Actor.PodUID != l.PodUID || m.Attribution.Provider != l.Provider || m.Attribution.RequestedModel != l.CaptureModel || m.Kind != r.Kind || m.Key != r.Key || m.ContentSHA256 != chunk.SHA256 || int(m.ByteCount) != chunk.Bytes || m.Completeness != intent.Completeness || !reflect.DeepEqual(m.ManagedOutput, expected) {
			return errManagedOutput
		}
		if captureJSON(c.dir, chunk.Key+"-ack.json", result) != nil {
			return errManagedOutput
		}
	}
	return nil
}

func (c *managedCapture) finish(l Launch, complete bool) error {
	if c == nil {
		return nil
	}
	intent, err := c.retain(l, complete)
	if err != nil {
		return errManagedOutput
	}
	if len(intent.Chunks) == 0 || !c.confirmed {
		return nil
	}
	s, err := LoadSettings(os.Getenv)
	if err != nil || filepath.Join(s.StateDir, "managed-output", c.invocation.Invocation) != c.dir || s.PodUID != l.PodUID {
		return errManagedOutput
	}
	h, err := NewHeartbeatClient(s, l.Session)
	if err != nil {
		return errManagedOutput
	}
	return c.publish(context.Background(), l, intent, h)
}
