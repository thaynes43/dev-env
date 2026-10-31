package agentrun

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// showOnce changes only this explicit read's transport and output validation.
// The caller's ordinary connection, identity and kube-context remain intact.
func (a *app) showOnce(ctx context.Context, c *conn, name string) error {
	hc := *c.http
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	one := *c
	one.http = &hc
	one.rejectOversizeResponse = true
	var raw json.RawMessage
	status, err := a.callWith(withSessionTaskClaims(ctx), &one, http.MethodGet, apiv1.SessionPath(url.PathEscape(name)), nil, nil, &raw, false)
	if err != nil {
		return err
	}
	var session apiv1.Session
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if status != http.StatusOK || !strictSessionJSON(raw) || d.Decode(&session) != nil || d.Decode(new(any)) != io.EOF || session.Name != name {
		return fail(ExitFailed, "show --once returned an unconfirmed named Session")
	}
	// Keep optional nulls, absent fields, integer zero and decimal strings exactly
	// as supplied. Original-canary UID/history fences belong to its caller.
	return a.printJSON(raw)
}

// Session contains a tolerant UsageObservation decoder for heartbeat liveness.
// Walk every canonical exported wire field before that decoder can hide an
// unknown key or nested type error. This validator grants no status authority.
func strictSessionJSON(raw []byte) bool {
	return strictWireJSON(raw, reflect.TypeFor[apiv1.Session]())
}

// strictWireJSON walks raw against the canonical exported JSON tags of the
// standard-library wire DTO root before any typed decode: it refuses duplicate
// keys, case aliases, unknown fields, trailing values, null scalars and nested
// type errors that encoding/json alone would let through. show --once, canary
// create, directory list and budget use it (DESIGN-023, history-05).
func strictWireJSON(raw []byte, root reflect.Type) bool {
	// send fences the complete wire body before decoding. Independently bound
	// the JSON value span here, even when this validator is called directly.
	if len(raw) == 0 || len(raw) >= maxResponse || !utf8.Valid(raw) {
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
		if t == reflect.TypeFor[time.Time]() {
			s, ok := tok.(string)
			if !ok {
				return false
			}
			_, err := time.Parse(time.RFC3339Nano, s)
			return err == nil
		}
		// The directory's reported time encodes an unknown time as null and a
		// recorded one as an RFC 3339 string (its own MarshalJSON).
		if t == reflect.TypeFor[apiv1.DirectoryReportedTime]() {
			if tok == nil {
				return true
			}
			s, ok := tok.(string)
			if !ok {
				return false
			}
			_, err := time.Parse(time.RFC3339, s)
			return err == nil
		}
		switch t.Kind() {
		case reflect.String:
			_, ok := tok.(string)
			return ok
		case reflect.Bool:
			_, ok := tok.(bool)
			return ok
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			n, ok := tok.(json.Number)
			if !ok {
				return false
			}
			_, err := strconv.ParseInt(string(n), 10, t.Bits())
			return err == nil
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			n, ok := tok.(json.Number)
			if !ok {
				return false
			}
			_, err := strconv.ParseUint(string(n), 10, t.Bits())
			return err == nil
		case reflect.Slice:
			if tok == nil {
				return true
			}
			if tok != json.Delim('[') {
				return false
			}
			for d.More() {
				if !read(t.Elem(), depth+1) {
					return false
				}
			}
			close, err := d.Token()
			return err == nil && close == json.Delim(']')
		case reflect.Struct:
			if tok != json.Delim('{') {
				return false
			}
			fields := make(map[string]reflect.StructField, t.NumField())
			for i := 0; i < t.NumField(); i++ {
				f := t.Field(i)
				name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				if f.IsExported() && name != "" && name != "-" {
					fields[name] = f
				}
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
		default:
			return false
		}
	}
	if !read(root, 0) {
		return false
	}
	_, err := d.Token()
	return err == io.EOF
}
