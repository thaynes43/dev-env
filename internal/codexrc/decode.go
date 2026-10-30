package codexrc

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"unicode/utf8"
)

const maxResponseBytes = 256 << 10

var errProtocol = errors.New("invalid pinned Codex discovery response")

// These wire shapes mirror only the two generated response fixtures. Optional
// nullable fields use pointers; omitempty marks fields not required by schema.
type statusResponse struct {
	EnvironmentID  *string `json:"environmentId,omitempty"`
	InstallationID string  `json:"installationId"`
	ServerName     string  `json:"serverName"`
	Status         string  `json:"status"`
}

type projectRoot struct {
	Path string `json:"path"`
}

type projectResponse struct {
	CreatedAt int64             `json:"createdAt"`
	ID        string            `json:"id"`
	Metadata  map[string]string `json:"metadata"`
	Name      string            `json:"name"`
	Position  int64             `json:"position"`
	RecencyAt *int64            `json:"recencyAt,omitempty"`
	Roots     []projectRoot     `json:"roots"`
	UpdatedAt int64             `json:"updatedAt"`
}

type projectsResponse struct {
	Data       []projectResponse `json:"data"`
	NextCursor *string           `json:"nextCursor,omitempty"`
}

func decodeStatus(raw []byte) (statusResponse, error) {
	var status statusResponse
	if strictDecode(raw, &status) != nil || !identifier(status.InstallationID) {
		return statusResponse{}, errProtocol
	}
	switch status.Status {
	case "disabled", "connecting", "connected", "errored":
		return status, nil
	default:
		return statusResponse{}, errProtocol
	}
}

func decodeProjects(pages []json.RawMessage) ([]projectResponse, error) {
	if len(pages) == 0 || len(pages) > 16 {
		return nil, errProtocol
	}
	var projects []projectResponse
	seenIDs, seenCursors := map[string]bool{}, map[string]bool{}
	total := 0
	for i, raw := range pages {
		total += len(raw)
		var page projectsResponse
		if total > maxResponseBytes || strictDecode(raw, &page) != nil || len(projects)+len(page.Data) > 1024 {
			return nil, errProtocol
		}
		if i == len(pages)-1 {
			if page.NextCursor != nil {
				return nil, errProtocol // incomplete inventory cannot prove uniqueness
			}
		} else {
			if page.NextCursor == nil || !identifier(*page.NextCursor) || seenCursors[*page.NextCursor] {
				return nil, errProtocol
			}
			seenCursors[*page.NextCursor] = true
		}
		for _, project := range page.Data {
			if !identifier(project.ID) || seenIDs[project.ID] || len(project.Roots) > 128 {
				return nil, errProtocol
			}
			seenIDs[project.ID] = true
			for _, root := range project.Roots {
				if !absoluteRoot(root.Path) {
					return nil, errProtocol
				}
			}
			projects = append(projects, project)
		}
	}
	return projects, nil
}

// Reject duplicate keys, aliases, null required fields, unknown fields, trailing
// documents and non-UTF-8 input. encoding/json alone accepts several of these.
// The local exact-field policy is deliberately stricter than draft-07's default
// additionalProperties behavior: a changed pinned protocol requires review.
func strictDecode(raw []byte, dst any) error {
	if len(raw) == 0 || len(raw) > maxResponseBytes || !utf8.Valid(raw) {
		return errProtocol
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if !uniqueKeys(d, 0) {
		return errProtocol
	}
	if _, err := d.Token(); !errors.Is(err, io.EOF) {
		return errProtocol
	}
	var shape any
	if json.Unmarshal(raw, &shape) != nil || !exactShape(shape, reflect.TypeOf(dst).Elem()) || json.Unmarshal(raw, dst) != nil {
		return errProtocol
	}
	return nil
}

func uniqueKeys(d *json.Decoder, depth int) bool {
	if depth > 16 {
		return false
	}
	token, err := d.Token()
	if err != nil {
		return false
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return true
	}
	keys := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || keys[name] {
				return false
			}
			keys[name] = true
		}
		if !uniqueKeys(d, depth+1) {
			return false
		}
	}
	end, err := d.Token()
	return err == nil && (delim == '{' && end == json.Delim('}') || delim == '[' && end == json.Delim(']'))
}

func exactShape(value any, typ reflect.Type) bool {
	if typ.Kind() == reflect.Pointer {
		return value == nil || exactShape(value, typ.Elem())
	}
	if value == nil {
		return false
	}
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		known := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			known[name] = true
			child, exists := object[name]
			if !exists {
				if options != "omitempty" {
					return false
				}
			} else if !exactShape(child, field.Type) {
				return false
			}
		}
		for key := range object {
			if !known[key] {
				return false
			}
		}
	case reflect.Slice:
		array, ok := value.([]any)
		if !ok {
			return false
		}
		for _, child := range array {
			if !exactShape(child, typ.Elem()) {
				return false
			}
		}
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, child := range object {
			if !exactShape(child, typ.Elem()) {
				return false
			}
		}
	}
	return true // scalar types and integer ranges are checked by json.Unmarshal
}
