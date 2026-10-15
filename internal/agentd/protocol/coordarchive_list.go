package protocol

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

const MaxCoordArchiveListItems = 100

// CoordArchiveListQuery selects one immutable index. Cursor values are opaque
// and bind the index's resource, project, and optional recipient selector.
type CoordArchiveListQuery struct {
	Resource     string
	Project      string
	RecipientUID string
	Limit        int
	After        string
}

func (q CoordArchiveListQuery) Validate() error {
	prefix, _ := coordArchiveResource(q.Resource)
	if prefix == "" || !coordArchiveLabel.MatchString(q.Project) || q.Limit < 1 || q.Limit > MaxCoordArchiveListItems {
		return HistoryErrorf(HistoryErrInvalid, "", "coordination list resource, project, or limit is invalid")
	}
	if q.RecipientUID != "" && (q.Resource != "inboxmessages" || !ValidKubeUID(q.RecipientUID)) {
		return HistoryErrorf(HistoryErrInvalid, "", "recipient selector is only valid for inbox messages")
	}
	if q.After != "" {
		if _, err := q.cursor(); err != nil {
			return err
		}
	}
	return nil
}

type coordArchiveListCursor struct {
	Resource     string `json:"r"`
	Project      string `json:"p"`
	RecipientUID string `json:"u,omitempty"`
	Name         string `json:"n"`
}

func (q CoordArchiveListQuery) cursor() (coordArchiveListCursor, error) {
	var c coordArchiveListCursor
	if len(q.After) > base64.RawURLEncoding.EncodedLen(512) {
		return coordArchiveListCursor{}, HistoryErrorf(HistoryErrInvalid, "", "coordination list cursor is invalid or belongs to another index")
	}
	raw, err := base64.RawURLEncoding.DecodeString(q.After)
	if err != nil || len(raw) == 0 || len(raw) > 512 || json.Unmarshal(raw, &c) != nil ||
		c.Resource != q.Resource || c.Project != q.Project || c.RecipientUID != q.RecipientUID || !validCoordArchiveIndexName(c.Name) {
		return coordArchiveListCursor{}, HistoryErrorf(HistoryErrInvalid, "", "coordination list cursor is invalid or belongs to another index")
	}
	return c, nil
}

func (q CoordArchiveListQuery) Cursor(name string) (string, error) {
	if !validCoordArchiveIndexName(name) {
		return "", HistoryErrorf(HistoryErrInvalid, "", "coordination index name is invalid")
	}
	raw, err := json.Marshal(coordArchiveListCursor{Resource: q.Resource, Project: q.Project, RecipientUID: q.RecipientUID, Name: name})
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// CoordArchiveListCursorName returns the last immutable index name after
// validating the cursor's complete selector binding.
func CoordArchiveListCursorName(q CoordArchiveListQuery) (string, error) {
	c, err := q.cursor()
	if err != nil {
		return "", err
	}
	return c.Name, nil
}

func validCoordArchiveIndexName(name string) bool {
	const timestampLayout = "20060102T150405.000000000Z"
	if len(name) != len(timestampLayout+"-")+36+len(".json") || !strings.HasSuffix(name, ".json") || name[len(timestampLayout)] != '-' {
		return false
	}
	timestamp, err := time.Parse(timestampLayout, name[:len(timestampLayout)])
	if err != nil || timestamp.Location() != time.UTC || timestamp.Format(timestampLayout) != name[:len(timestampLayout)] {
		return false
	}
	return ValidKubeUID(strings.TrimSuffix(name[len(timestampLayout)+1:], ".json"))
}

func ValidCoordArchiveIndexName(name string) bool { return validCoordArchiveIndexName(name) }

func CoordArchiveIndexNameUID(name string) string {
	if !validCoordArchiveIndexName(name) {
		return ""
	}
	return strings.TrimSuffix(name[len("20060102T150405.000000000Z")+1:], ".json")
}

// CoordArchiveListItem intentionally exposes no unverified object. Corrupt
// selected revisions are represented in-band so one damaged UID cannot make a
// bounded page unobservable.
type CoordArchiveListItem struct {
	UID      string                      `json:"uid"`
	Error    string                      `json:"error,omitempty"`
	Manifest *CoordArchiveStoredManifest `json:"manifest,omitempty"`
	Object   json.RawMessage             `json:"object,omitempty"`
}

type CoordArchiveListResult struct {
	Items []CoordArchiveListItem `json:"items"`
	Next  string                 `json:"next,omitempty"`
}
