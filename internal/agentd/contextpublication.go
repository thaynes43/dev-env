package agentd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"syscall"

	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

var contextProjectPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var contextDigestPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// The fixed operator endpoint and rotating projected identity are the only
// remote authority. Error bodies and redirect destinations are never exposed.
func (h *HeartbeatClient) contextRequest(ctx context.Context, method, path string, body any, result any) error {
	u, err := url.Parse(h.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || h.HTTP == nil {
		return errors.New("published context endpoint unavailable")
	}
	tok, err := os.ReadFile(h.TokenFile)
	if err != nil || len(tok) == 0 || len(tok) > 16<<10 {
		return errors.New("published context identity unavailable")
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return errors.New("published context request invalid")
		}
	}
	ctx, cancel := context.WithTimeout(ctx, heartbeatTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(h.URL, "/")+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("published context request invalid")
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(tok)))
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	hc := *h.HTTP
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := hc.Do(req)
	if err != nil {
		return errors.New("published context acknowledgement is unconfirmed")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return errors.New("published context request refused or unconfirmed")
	}
	data, err = io.ReadAll(io.LimitReader(resp.Body, apiv1.MaxContextRequestBytes+1))
	if err != nil || len(data) > apiv1.MaxContextRequestBytes {
		return errors.New("published context response exceeds bound")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(result) != nil || !errors.Is(d.Decode(new(any)), io.EOF) {
		return errors.New("published context response invalid")
	}
	return nil
}

func validContextMetadata(m apiv1.ContextMetadata) bool {
	return apiv1.ValidContextUID(m.Source.UID) && m.Name == apiv1.ContextName(m.Source.UID, m.Kind, m.Key) && apiv1.ValidContextUID(m.UID) &&
		apiv1.ValidContextRequest(apiv1.PublishContextRequest{Kind: m.Kind, Key: m.Key, Text: "probe"}) && m.SourceGeneration >= 1 && m.Source.Name != "" && m.Source.Namespace != "" &&
		m.Actor.Kind == "session" && apiv1.ValidContextUID(m.Actor.PodUID) && m.Actor.PodName != "" && m.Actor.ServiceAccount != "" && contextProjectPattern.MatchString(m.Project) && contextProjectPattern.MatchString(m.Repository) &&
		contextProjectPattern.MatchString(m.Attribution.Provider) && len(m.Attribution.RequestedModel) > 0 && len(m.Attribution.RequestedModel) <= 256 &&
		contextDigestPattern.MatchString(m.ContentSHA256) && m.ByteCount > 0 && m.ByteCount <= apiv1.MaxContextBytes && !m.PublishedAt.IsZero() &&
		contextDigestPattern.MatchString(m.Provenance.CatalogRevision) && contextDigestPattern.MatchString(m.Provenance.RulesRevision) && m.Provenance.InitialPinState == "Unknown" && m.Provenance.ReportedHead == "" && m.Provenance.ReportedAt == nil &&
		m.Attribution.ObservedModelState == "Unknown" && m.Attribution.UsageState == "Unknown" && m.Attribution.CostState == "Unknown" && m.StopState == "Unknown" && m.Origin == "ExplicitPublished" && m.Completeness == "ExplicitExcerpt"
}

type contextIntent struct {
	Session       string `json:"session"`
	SessionUID    string `json:"sessionUID"`
	PodUID        string `json:"podUID"`
	Kind          string `json:"kind"`
	Key           string `json:"key"`
	ContentSHA256 string `json:"contentSHA256"`
	ByteCount     int    `json:"byteCount"`
}

func retainContextIntent(s Settings, sess protocol.Session, req apiv1.PublishContextRequest) error {
	intent := contextIntent{sess.Name, sess.SessionUID, s.PodUID, req.Kind, req.Key, apiv1.ContextHash(req.Text), len(req.Text)}
	file := apiv1.ContextName(sess.SessionUID, req.Kind, req.Key) + ".json"
	data, err := json.Marshal(intent)
	if err != nil {
		return errors.New("publication intent invalid")
	}
	if err = projectSyncExclusiveFile(s.statePath(file), data); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrExist) {
		return errors.New("publication intent is unconfirmed")
	}
	// An uncertain local write must match a complete private original record;
	// absence or a changed key/body never grants a second publication intent.
	f, err := os.OpenFile(s.statePath(file), os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return errors.New("publication intent is unconfirmed")
	}
	defer func() { _ = f.Close() }()
	fi, statErr := f.Stat()
	if statErr != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0o600 || fi.Size() > 4096 {
		return errors.New("publication intent is unconfirmed")
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != uint32(os.Getuid()) || st.Nlink != 1 {
		return errors.New("publication intent ownership is unconfirmed")
	}
	var saved contextIntent
	d := json.NewDecoder(io.LimitReader(f, 4096))
	d.DisallowUnknownFields()
	if d.Decode(&saved) != nil || !errors.Is(d.Decode(new(any)), io.EOF) || !reflect.DeepEqual(intent, saved) || f.Sync() != nil {
		return errors.New("publication intent differs or is unconfirmed")
	}
	dir, err := os.Open(s.StateDir)
	if err != nil {
		return errors.New("publication intent durability unconfirmed")
	}
	defer func() { _ = dir.Close() }()
	if dir.Sync() != nil {
		return errors.New("publication intent durability unconfirmed")
	}
	return nil
}

// PublishContext consumes explicit stdin text; it calls no provider, Runner or
// native RPC and imports no path. Its durable intent contains no text or token.
func PublishContext(ctx context.Context, s Settings, sess protocol.Session, h *HeartbeatClient, req apiv1.PublishContextRequest) (apiv1.PublishContextResult, error) {
	var result apiv1.PublishContextResult
	if !apiv1.ValidContextRequest(req) || !apiv1.ValidContextUID(sess.SessionUID) || !apiv1.ValidContextUID(s.PodUID) || h == nil || h.Session != sess.Name {
		return result, errors.New("publication requires bounded text and this pod's original session identity")
	}
	unlock, err := privateNativeLock(s, "published-context.lock")
	if err != nil {
		return result, errors.New("private publication intent unavailable")
	}
	defer unlock()
	if retainContextIntent(s, sess, req) != nil {
		return result, errors.New("private publication intent differs or is unconfirmed")
	}
	if err = h.contextRequest(ctx, http.MethodPost, apiv1.PublishedContextsPath, req, &result); err != nil {
		return result, err
	}
	m := result.Metadata
	if (result.Outcome != "Created" && result.Outcome != "Confirmed") || !validContextMetadata(m) || m.Source.Name != sess.Name || m.Source.UID != sess.SessionUID || m.Actor.PodUID != s.PodUID || m.Kind != req.Kind || m.Key != req.Key ||
		m.ContentSHA256 != apiv1.ContextHash(req.Text) || int(m.ByteCount) != len(req.Text) || m.Attribution.Provider != sess.Agent || m.Attribution.RequestedModel != sess.Model {
		return apiv1.PublishContextResult{}, errors.New("publication acknowledgement does not confirm original content and identity")
	}
	return result, nil
}

func (h *HeartbeatClient) ReadPublishedContext(ctx context.Context, project, name string) (apiv1.PublishedContext, error) {
	var result apiv1.PublishedContext
	if !contextProjectPattern.MatchString(project) || !apiv1.ValidContextName(name) {
		return result, errors.New("published context selection invalid")
	}
	if err := h.contextRequest(ctx, http.MethodGet, apiv1.PublishedContextsPath+"/"+name+"?project="+url.QueryEscape(project), nil, &result); err != nil {
		return result, err
	}
	m := result.Metadata
	if !validContextMetadata(m) || m.Name != name || m.Project != project || !apiv1.ValidContextRequest(apiv1.PublishContextRequest{Kind: m.Kind, Key: m.Key, Text: result.Text}) || m.ContentSHA256 != apiv1.ContextHash(result.Text) || int(m.ByteCount) != len(result.Text) {
		return apiv1.PublishedContext{}, errors.New("published context content or provenance unconfirmed")
	}
	return result, nil
}

func (h *HeartbeatClient) ListPublishedContexts(ctx context.Context, project, cursor string, limit int) (apiv1.PublishedContextList, error) {
	var result apiv1.PublishedContextList
	if !contextProjectPattern.MatchString(project) || limit < 1 || limit > 20 || len(cursor) > 4096 {
		return result, errors.New("published context selection invalid")
	}
	q := url.Values{"project": {project}, "limit": {strconv.Itoa(limit)}, "continue": {cursor}}
	if err := h.contextRequest(ctx, http.MethodGet, apiv1.PublishedContextsPath+"?"+q.Encode(), nil, &result); err != nil {
		return result, err
	}
	if len(result.Items) > limit || len(result.Continue) > 4096 {
		return apiv1.PublishedContextList{}, errors.New("published context list exceeds bound")
	}
	for _, m := range result.Items {
		if !validContextMetadata(m) || m.Project != project {
			return apiv1.PublishedContextList{}, errors.New("published context list provenance unconfirmed")
		}
	}
	return result, nil
}
