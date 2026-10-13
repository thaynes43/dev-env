package agentrun

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

func (a *app) canaryResult(ctx context.Context, args []string) error {
	cmd := newCommand("canary", outputJSON)
	cmd.c.output = outputJSON
	var id, uid string
	var once, asJSON bool
	cmd.fs.StringVar(&id, "id", "", "")
	cmd.fs.StringVar(&uid, "session-uid", "", "")
	cmd.fs.BoolVar(&once, "once", false, "")
	cmd.fs.BoolVar(&asJSON, "json", false, "")
	counts := map[string]int{}
	for _, arg := range args {
		for _, flag := range []string{"id", "session-uid", "context"} {
			if arg == "--"+flag || arg == "-"+flag || strings.HasPrefix(arg, "--"+flag+"=") || strings.HasPrefix(arg, "-"+flag+"=") {
				counts[flag]++
			}
		}
	}
	pos, err := cmd.parse(a, args)
	if err != nil {
		return err
	}
	// On this command only, --context selects a publication, not a kubeconfig
	// context. Consume and clear it before connection/auth configuration is read.
	name := cmd.c.conn.context
	cmd.c.conn.context = ""
	if len(pos) != 0 || !once || !asJSON || counts["id"] != 1 || counts["session-uid"] != 1 || counts["context"] != 1 || !canaryName.MatchString(id) || !apiv1.ValidContextUID(uid) || !apiv1.ValidContextName(name) {
		return usageError("canary result requires one --id, --session-uid and --context, plus --json --once, with no task options or positional arguments")
	}
	c, err := a.connect(ctx, cmd.c.conn)
	if err != nil {
		return err
	}
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	var raw json.RawMessage
	status, err := a.callWith(ctx, &one, http.MethodGet, apiv1.CanaryResultPath(id), url.Values{"sessionUID": {uid}, "context": {name}}, nil, &raw, false)
	if err != nil {
		return err
	}
	var result apiv1.CanaryResult
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if status != http.StatusOK || !strictCanaryResultJSON(raw) || d.Decode(&result) != nil || d.Decode(new(any)) != io.EOF || !confirmedCanaryResult(id, uid, name, result) {
		return fail(ExitFailed, "canary result publication or original identity is unconfirmed")
	}
	// Preserve the validated raw reply, including explicit optional nulls.
	return a.printJSON(raw)
}

// encoding/json's struct decoder alone permits duplicates, case aliases and
// null scalars. Walk the exact wire DTO before preserving its raw JSON. Every
// required field must appear once; only explicitly optional pointers may null.
func strictCanaryResultJSON(raw []byte) bool {
	if len(raw) > apiv1.MaxContextRequestBytes || !utf8.Valid(raw) {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func(reflect.Type, int) bool
	read = func(t reflect.Type, depth int) bool {
		if depth > 16 {
			return false
		}
		tok, err := d.Token()
		if err != nil {
			return false
		}
		if t.Kind() == reflect.Pointer {
			if tok == nil {
				return true
			}
			t = t.Elem()
		}
		if t == reflect.TypeFor[time.Time]() || t.Kind() == reflect.String {
			_, ok := tok.(string)
			return ok
		}
		if t.Kind() == reflect.Int32 || t.Kind() == reflect.Int64 {
			_, ok := tok.(json.Number)
			return ok
		}
		// The only list is the bounded observed model list: an array, never
		// null, of at most MaxServedModels elements.
		if t.Kind() == reflect.Slice {
			if tok != json.Delim('[') {
				return false
			}
			for n := 0; d.More(); n++ {
				if n == protocol.MaxServedModels || !read(t.Elem(), depth+1) {
					return false
				}
			}
			end, err := d.Token()
			return err == nil && end == json.Delim(']')
		}
		if t.Kind() != reflect.Struct || tok != json.Delim('{') {
			return false
		}
		fields := make(map[string]reflect.StructField, t.NumField())
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			fields[name] = f
		}
		seen := make(map[string]bool, len(fields))
		for d.More() {
			key, err := d.Token()
			name, ok := key.(string)
			f, known := fields[name]
			if err != nil || !ok || !known || seen[name] || !read(f.Type, depth+1) {
				return false
			}
			seen[name] = true
		}
		for name, f := range fields {
			if !seen[name] && !strings.Contains(f.Tag.Get("json"), ",omitempty") {
				return false
			}
		}
		close, err := d.Token()
		return err == nil && close == json.Delim('}')
	}
	if !read(reflect.TypeFor[apiv1.CanaryResult](), 0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}

func confirmedCanaryResult(id, uid, name string, result apiv1.CanaryResult) bool {
	p, m := result.Context, result.Context.Metadata
	parent := strings.Split(m.SourceParent, "/")
	if len(parent) != 2 || !canaryName.MatchString(parent[0]) || !canaryName.MatchString(parent[1]) || !apiv1.ValidManagedOutput(m.ManagedOutput) {
		return false
	}
	digest := sha256.Sum256([]byte(m.SourceParent + "\x00" + id))
	sourceName := "private-canary-" + hex.EncodeToString(digest[:])[:32]
	actor := strings.Split(m.Actor.ServiceAccount, "/")
	return result.CanaryID == id && result.SessionUID == uid && m.Name == name && apiv1.ValidContextUID(m.UID) && m.Source.UID == uid && m.Source.Name == sourceName && canaryName.MatchString(m.Source.Namespace) && m.SourceGeneration >= 1 &&
		m.Kind == "transcript-excerpt" && m.Name == apiv1.ContextName(uid, m.Kind, m.Key) && m.Key == apiv1.ManagedOutputKey(m.ManagedOutput.Invocation, m.ManagedOutput.Ordinal) && m.ManagedOutput.Invocation == apiv1.ManagedInvocation(uid, m.Actor.PodUID) &&
		m.Actor.Kind == "session" && m.Actor.PodName == sourceName && apiv1.ValidContextUID(m.Actor.PodUID) && len(actor) == 2 && actor[0] == m.Source.Namespace && canaryName.MatchString(actor[1]) &&
		canaryName.MatchString(m.Project) && canaryName.MatchString(m.Repository) && (m.Attribution.Provider == "claude" || m.Attribution.Provider == "codex") && len(m.Attribution.RequestedModel) > 0 && len(m.Attribution.RequestedModel) <= 256 &&
		apiv1.ValidContextAttribution(m.Attribution, true) && m.StopState == "Unknown" && m.Provenance.InitialPinState == "Unknown" && m.Provenance.ReportedHead == "" && m.Provenance.ReportedAt == nil &&
		len(m.Provenance.CatalogRevision) == 64 && len(m.Provenance.RulesRevision) == 64 && m.Origin == "ManagedOutputAssertion" && (m.Completeness == "PartialManagedOutput" || m.Completeness == "Unknown") && !m.PublishedAt.IsZero() &&
		apiv1.ValidContextRequest(apiv1.PublishContextRequest{Kind: m.Kind, Key: m.Key, Text: p.Text}) && m.ContentSHA256 == apiv1.ContextHash(p.Text) && int(m.ByteCount) == len(p.Text)
}
