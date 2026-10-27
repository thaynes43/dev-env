package agentd

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
)

const portalProjectionInputLimit = 1 << 20
const portalProjectionLineLimit = 64 << 10
const portalProjectionTextLimit = 4 << 10
const portalProjectionTotalLimit = 96 << 10
const portalProjectionTurnLimit = 60

var portalKnownSecret = regexp.MustCompile(`(?i)(bearer[[:space:]]+|(?:api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret)[[:space:]]*[:=][[:space:]]*)("[^"\n]*"|'[^'\n]*'|[^[:space:]"'<>]+)`)
var portalKeyToken = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{12,}|gh[opurs]_[A-Za-z0-9_]{12,})\b`)

// projectPortalConversationBefore projects only recognized visible fields.
func projectPortalConversationBefore(provider, conversation string, raw []byte, observed time.Time, before uint64) protocol.PortalConversation {
	result := protocol.PortalConversation{State: "unavailable", ReasonCode: "unsupported", ObservedAt: observed.UTC()}
	if (provider != protocol.AgentClaude && provider != protocol.AgentCodex) || !nativeThreadID.MatchString(conversation) || len(raw) > portalProjectionInputLimit || observed.IsZero() {
		return result
	}
	result.State = "partial"
	result.ReasonCode = "coverage_unverified"
	result.ConversationID = conversation
	seen := map[string]string{}
	var total int
	var previous time.Time
	lines := bytes.Split(raw, []byte("\n"))
	for index, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if len(line) > portalProjectionLineLimit {
			result.State = "truncated"
			result.ReasonCode = "source_limit"
			continue
		}
		id, role, text, at, visible, ok := portalVisibleRecord(provider, conversation, line)
		if !ok {
			if result.State != "truncated" {
				result.State = "partial"
				result.ReasonCode = "unrecognized_record"
			}
			continue
		}
		if !visible {
			continue
		}
		identity := role + "\x00" + at.UTC().Format(time.RFC3339Nano) + "\x00" + text
		if old, exists := seen[id]; exists {
			if old != identity {
				return protocol.PortalConversation{State: "unavailable", ReasonCode: "conflicting_record", ObservedAt: observed.UTC()}
			}
			continue
		}
		if previous.After(at) {
			return protocol.PortalConversation{State: "unavailable", ReasonCode: "ordering", ObservedAt: observed.UTC()}
		}
		seen[id] = identity
		previous = at
		if before != 0 && uint64(index+1) >= before {
			continue
		}
		text = portalKnownSecret.ReplaceAllString(text, "${1}[redacted]")
		text = portalKeyToken.ReplaceAllString(text, "[redacted]")
		truncated := false
		if len(text) > portalProjectionTextLimit {
			text = portalUTF8Prefix(text, portalProjectionTextLimit)
			truncated = true
			result.State = "truncated"
			result.ReasonCode = "source_limit"
		}
		for len(result.Turns) > 0 && (len(result.Turns) >= portalProjectionTurnLimit || total+len(text) > portalProjectionTotalLimit) {
			total -= len(result.Turns[0].Text)
			result.Turns = result.Turns[1:]
			result.State = "truncated"
			result.ReasonCode = "source_limit"
		}
		total += len(text)
		result.Turns = append(result.Turns, protocol.PortalConversationTurn{Seq: uint64(index + 1), At: at, Role: role, Text: text, Truncated: truncated})
	}
	if len(result.Turns) > 0 {
		result.OldestSeq = result.Turns[0].Seq
		result.NewestSeq = result.Turns[len(result.Turns)-1].Seq
	}
	return result
}

func portalUTF8Prefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

func portalVisibleRecord(provider, conversation string, line []byte) (string, string, string, time.Time, bool, bool) {
	var envelope struct {
		Type      string          `json:"type"`
		Timestamp string          `json:"timestamp"`
		UUID      string          `json:"uuid"`
		SessionID string          `json:"sessionId"`
		Sidechain bool            `json:"isSidechain"`
		APIError  bool            `json:"isApiErrorMessage"`
		Message   json.RawMessage `json:"message"`
		Payload   json.RawMessage `json:"payload"`
	}
	if !utf8.Valid(line) || !protocol.StrictPortalJSON(line) || !portalCanonicalFields(line, "type", "timestamp", "uuid", "sessionId", "isSidechain", "isApiErrorMessage", "message", "payload", "version", "cwd") || json.Unmarshal(line, &envelope) != nil {
		return "", "", "", time.Time{}, false, false
	}
	if provider == protocol.AgentClaude {
		if envelope.Type != "user" && envelope.Type != "assistant" {
			return "", "", "", time.Time{}, false, true
		}
		if envelope.Sidechain || envelope.APIError {
			return "", "", "", time.Time{}, false, true
		}
		if envelope.SessionID != conversation || envelope.UUID == "" {
			return "", "", "", time.Time{}, false, false
		}
		var msg struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if !portalCanonicalFields(envelope.Message, "role", "content") || json.Unmarshal(envelope.Message, &msg) != nil || msg.Role != envelope.Type {
			return "", "", "", time.Time{}, false, false
		}
		text, ok := portalTextBlocks(msg.Content, envelope.Type, provider)
		if !ok {
			return "", "", "", time.Time{}, false, false
		}
		at, err := time.Parse(time.RFC3339Nano, envelope.Timestamp)
		if err != nil {
			return "", "", "", time.Time{}, false, false
		}
		if text == "" {
			return "", "", "", time.Time{}, false, true
		}
		return envelope.UUID, envelope.Type, text, at, true, true
	}
	if envelope.Type != "response_item" {
		return "", "", "", time.Time{}, false, true
	}
	var item struct {
		ID      string          `json:"id"`
		Type    string          `json:"type"`
		Role    string          `json:"role"`
		Channel string          `json:"channel"`
		Content json.RawMessage `json:"content"`
	}
	if !portalCanonicalFields(envelope.Payload, "id", "type", "role", "channel", "content") || json.Unmarshal(envelope.Payload, &item) != nil {
		return "", "", "", time.Time{}, false, false
	}
	if item.Type != "message" || (item.Role != "user" && item.Role != "assistant") {
		return "", "", "", time.Time{}, false, true
	}
	// Only explicitly final assistant messages are qualified; channel-less user
	// input is accepted, while unknown channels and missing assistant channels are omitted.
	if (item.Role == "assistant" && item.Channel != "final") || (item.Role == "user" && item.Channel != "") {
		return "", "", "", time.Time{}, false, true
	}
	if item.ID == "" {
		return "", "", "", time.Time{}, false, false
	}
	text, ok := portalTextBlocks(item.Content, item.Role, provider)
	if !ok {
		return "", "", "", time.Time{}, false, false
	}
	at, err := time.Parse(time.RFC3339Nano, envelope.Timestamp)
	if err != nil {
		return "", "", "", time.Time{}, false, false
	}
	if text == "" {
		return "", "", "", time.Time{}, false, true
	}
	return item.ID, item.Role, text, at, true, true
}

func portalTextBlocks(raw json.RawMessage, role, provider string) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		if provider != protocol.AgentClaude {
			return "", false
		}
		return portalSafeText(text), true
	}
	var entries []json.RawMessage
	if json.Unmarshal(raw, &entries) != nil {
		return "", false
	}
	var parts []string
	for _, entry := range entries {
		var b struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if !portalCanonicalFields(entry, "type", "text") || json.Unmarshal(entry, &b) != nil {
			return "", false
		}
		if (provider == protocol.AgentClaude && b.Type == "text") || (provider == protocol.AgentCodex && role == "user" && b.Type == "input_text") || (provider == protocol.AgentCodex && role == "assistant" && b.Type == "output_text") {
			if x := portalSafeText(b.Text); x != "" {
				parts = append(parts, x)
			}
		}
	}
	return strings.Join(parts, "\n"), true
}

func portalSafeText(s string) string {
	if !utf8.ValidString(s) {
		return ""
	}
	lower := strings.ToLower(s)
	for _, marker := range []string{"<system-reminder", "<local-command", "<environment_context", "<user_instructions", "<permissions instructions", "<skills_instructions", "<developer", "# agents.md instructions"} {
		if strings.Contains(lower, marker) {
			return ""
		}
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

// portalCanonicalFields rejects case-fold aliases of interpreted fields while
// preserving unrelated provider metadata keys, including case-sensitive maps.
func portalCanonicalFields(raw []byte, names ...string) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return false
	}
	for key := range fields {
		for _, name := range names {
			if key != name && strings.EqualFold(key, name) {
				return false
			}
		}
	}
	return true
}

// projectRegisteredPortalConversationPage checks registration around the supplied reader.
func projectRegisteredPortalConversationPage(settings Settings, session protocol.Session, expectedSession, expectedPod string, observed time.Time, limit int, before string, read func(Launch, Launch) ([]byte, error)) protocol.PortalConversation {
	unavailable := func() protocol.PortalConversation {
		return protocol.PortalConversation{State: "unavailable", ReasonCode: "identity_unconfirmed", ObservedAt: observed.UTC()}
	}
	if expectedSession == "" || expectedPod == "" || session.Repo != "dev-env" || session.SessionUID != expectedSession || settings.PodUID != expectedPod || read == nil || observed.IsZero() {
		return unavailable()
	}
	current, currentErr := privateCurrentLaunch(settings)
	if currentErr != nil || !portalLaunchBound(current, session, expectedPod) {
		return unavailable()
	}
	var first Launch
	if readPrivateManagedJSON(settings, launchFile, 512<<10, &first) != nil || first.Session != session.Name || first.SessionUID != expectedSession || first.PodUID == "" || first.Provider != session.Agent || first.ConversationID != current.ConversationID || first.Dir != current.Dir || !nativeThreadID.MatchString(first.ConversationID) {
		return unavailable()
	}
	if session.Agent == protocol.AgentCodex && (!first.NativeThreadConfirmed || !current.NativeThreadConfirmed) {
		return unavailable()
	}
	raw, err := read(current, first)
	if err != nil {
		return unavailable()
	}
	after, afterErr := privateCurrentLaunch(settings)
	var afterFirst Launch
	if afterErr != nil || !reflect.DeepEqual(current, after) || readPrivateManagedJSON(settings, launchFile, 512<<10, &afterFirst) != nil || !reflect.DeepEqual(first, afterFirst) {
		return unavailable()
	}
	if limit < 1 || limit > 60 {
		return unavailable()
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte("dev-env/portal-history-cursor/v1\x00" + expectedSession + "\x00" + expectedPod + "\x00" + current.ConversationID + "\x00"))
	_, _ = digest.Write(raw)
	snapshot := digest.Sum(nil)
	var boundary uint64
	if before != "" {
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(before)
		if err != nil || len(decoded) != 40 || !bytes.Equal(decoded[:32], snapshot) {
			return unavailable()
		}
		boundary = binary.BigEndian.Uint64(decoded[32:])
		if boundary == 0 {
			return unavailable()
		}
	}
	result := projectPortalConversationBefore(session.Agent, current.ConversationID, raw, observed, boundary)
	if len(result.Turns) > limit {
		result.Turns = result.Turns[len(result.Turns)-limit:]
		result.State = "truncated"
		result.ReasonCode = "source_limit"
	}
	if len(result.Turns) > 0 {
		result.OldestSeq = result.Turns[0].Seq
		result.NewestSeq = result.Turns[len(result.Turns)-1].Seq
		if result.OldestSeq > 1 {
			cursor := make([]byte, 40)
			copy(cursor, snapshot)
			binary.BigEndian.PutUint64(cursor[32:], result.OldestSeq)
			result.NextCursor = base64.RawURLEncoding.EncodeToString(cursor)
		}
	}
	return result
}

func portalLaunchBound(l Launch, session protocol.Session, expectedPod string) bool {
	return l.TUI && l.Session == session.Name && l.SessionUID == session.SessionUID && l.PodUID == expectedPod && l.Provider == session.Agent && l.BootID != "" && l.Dir != "" && filepath.IsAbs(l.Dir) && filepath.Clean(l.Dir) == l.Dir && nativeThreadID.MatchString(l.ConversationID)
}

// PortalConversation is unavailable unless the exact current and initial
// registrations bind the selected Session. It never falls back to logs.
func PortalConversation(settings Settings, session protocol.Session, expectedSession, expectedPod string, limit int, before string, now time.Time) protocol.PortalConversation {
	unavailable := protocol.PortalConversation{State: "unavailable", ReasonCode: "unsupported", ObservedAt: now.UTC()}
	if limit < 1 || limit > 60 {
		return unavailable
	}
	result := projectRegisteredPortalConversationPage(settings, session, expectedSession, expectedPod, now, limit, before, func(current, first Launch) ([]byte, error) {
		if current.Dir != settings.WorktreePath(session.Name) {
			return nil, os.ErrPermission
		}
		raw, err := portalReadOwnTranscript(settings, first)
		if err != nil || !portalSourceVersion(session.Agent, first.ConversationID, first.Dir, raw) {
			return nil, os.ErrPermission
		}
		return raw, nil
	})
	return result
}

// portalReadOwnTranscript selects only a fixed provider-owned location derived
// from the immutable first launch. Directory descriptors never follow links.
func portalReadOwnTranscript(s Settings, first Launch) ([]byte, error) {
	if !nativeThreadID.MatchString(first.ConversationID) || first.CreatedAt.IsZero() || first.Dir == "" || !filepath.IsAbs(first.Dir) {
		return nil, os.ErrPermission
	}
	var directory, file string
	switch first.Provider {
	case protocol.AgentClaude:
		if s.ClaudeConfigDir != filepath.Join(s.Home, ".claude") {
			return nil, os.ErrPermission
		}
		directory = filepath.Join(s.ClaudeConfigDir, "projects", claudeProjectKey(first.Dir))
		file = first.ConversationID + ".jsonl"
	case protocol.AgentCodex:
		if codexHomeOf(first) != filepath.Join(s.Home, ".codex") {
			return nil, os.ErrPermission
		}
		directory = filepath.Join(s.Home, ".codex", "sessions", first.CreatedAt.UTC().Format("2006/01/02"))
	default:
		return nil, os.ErrPermission
	}
	dir, err := portalOpenDirectory(directory)
	if err != nil {
		return nil, err
	}
	defer func() { _ = dir.Close() }()
	if first.Provider == protocol.AgentCodex {
		entries, readErr := dir.ReadDir(1025)
		if readErr != nil && readErr != io.EOF {
			return nil, os.ErrPermission
		}
		if len(entries) > 1024 {
			return nil, os.ErrPermission
		}
		suffix := "-" + first.ConversationID + ".jsonl"
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "rollout-") && strings.HasSuffix(entry.Name(), suffix) {
				if file != "" {
					return nil, os.ErrPermission
				}
				file = entry.Name()
			}
		}
		if file == "" {
			return nil, os.ErrNotExist
		}
	}
	fd, err := syscall.Openat(int(dir.Fd()), file, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), "provider-record")
	defer func() { _ = f.Close() }()
	var before, after syscall.Stat_t
	if syscall.Fstat(fd, &before) != nil || before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Nlink != 1 || before.Size < 0 || before.Size > portalProjectionInputLimit {
		return nil, os.ErrPermission
	}
	raw, err := io.ReadAll(io.LimitReader(f, portalProjectionInputLimit+1))
	if err != nil || len(raw) > portalProjectionInputLimit || syscall.Fstat(fd, &after) != nil || before.Dev != after.Dev || before.Ino != after.Ino || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim || before.Nlink != after.Nlink {
		return nil, os.ErrPermission
	}
	return raw, nil
}

func portalOpenDirectory(path string) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, os.ErrPermission
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			_ = syscall.Close(fd)
			return nil, os.ErrPermission
		}
		next, e := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		_ = syscall.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), "provider-directory"), nil
}

// Only formats tied to a pinned CLI source version are eligible. Unknown
// formats remain unavailable; even supported fixtures report partial coverage.
func portalSourceVersion(provider, conversation, worktree string, raw []byte) bool {
	seenClaude := false
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if len(line) > portalProjectionLineLimit || !protocol.StrictPortalJSON(line) || !portalCanonicalFields(line, "type", "version", "sessionId", "cwd", "payload") {
			return false
		}
		var record struct {
			Type      string `json:"type"`
			Version   string `json:"version"`
			SessionID string `json:"sessionId"`
			CWD       string `json:"cwd"`
			Payload   struct {
				ID         string `json:"id"`
				CLIVersion string `json:"cli_version"`
				CWD        string `json:"cwd"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &record) != nil {
			return false
		}
		if provider == protocol.AgentCodex {
			var fields map[string]json.RawMessage
			if json.Unmarshal(line, &fields) != nil || !portalCanonicalFields(fields["payload"], "id", "cli_version", "cwd") {
				return false
			}
			return record.Type == "session_meta" && record.Payload.ID == conversation && record.Payload.CLIVersion == managedCodexVersion && record.Payload.CWD == worktree
		}
		if provider == protocol.AgentClaude && (record.Type == "user" || record.Type == "assistant") {
			if record.Version != "2.1.296" || record.SessionID != conversation || record.CWD != worktree {
				return false
			}
			seenClaude = true
		}
	}
	return provider == protocol.AgentClaude && seenClaude
}
