package asks

import (
	"encoding/base64"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/thaynes43/dev-env/internal/apiserver/apiv1"
)

// Page kinds. A cursor is valid for one kind and one limit.
const (
	PageMine = "mine"
	PageOpen = "open"
	PageAll  = "all"
)

// Page returns one newest-first page of items. Order is createdAt descending,
// then id descending. The cursor is opaque and is the last item of the previous
// page; a cursor from another kind or limit is refused.
func Page(items []apiv1.Ask, kind string, limit int, cursor string) (apiv1.AskList, error) {
	if limit < 1 || limit > apiv1.AskListMaxLimit {
		return apiv1.AskList{}, fmt.Errorf("%w: limit: must be 1 to 100", apiv1.ErrAskInvalid)
	}
	if kind != PageMine && kind != PageOpen && kind != PageAll {
		return apiv1.AskList{}, fmt.Errorf("%w: cursor: unknown list", apiv1.ErrAskInvalid)
	}
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, func(a, b apiv1.Ask) int {
		if a.CreatedAt.After(b.CreatedAt) {
			return -1
		}
		if a.CreatedAt.Before(b.CreatedAt) {
			return 1
		}
		return strings.Compare(b.ID, a.ID)
	})
	start := 0
	if cursor != "" {
		sec, id, err := decodeCursor(kind, limit, cursor)
		if err != nil {
			return apiv1.AskList{}, err
		}
		for start < len(sorted) && !olderThan(sorted[start], sec, id) {
			start++
		}
	}
	end := start + limit
	complete := end >= len(sorted)
	if complete {
		end = len(sorted)
	}
	page := append([]apiv1.Ask(nil), sorted[start:end]...)
	if page == nil {
		page = []apiv1.Ask{}
	}
	out := apiv1.AskList{Asks: page, Complete: complete}
	if !complete {
		out.NextCursor = encodeCursor(kind, limit, page[len(page)-1])
	}
	return out, nil
}

func olderThan(a apiv1.Ask, sec int64, id string) bool {
	as := a.CreatedAt.UTC().Unix()
	return as < sec || (as == sec && a.ID < id)
}

func encodeCursor(kind string, limit int, last apiv1.Ask) string {
	raw := fmt.Sprintf("v1\t%d\t%s\t%d\t%s", limit, kind, last.CreatedAt.UTC().Unix(), last.ID)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(kind string, limit int, cursor string) (int64, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, "", fmt.Errorf("%w: cursor: malformed", apiv1.ErrAskInvalid)
	}
	parts := strings.Split(string(raw), "\t")
	if len(parts) != 5 || parts[0] != "v1" {
		return 0, "", fmt.Errorf("%w: cursor: malformed", apiv1.ErrAskInvalid)
	}
	gotLimit, err := strconv.Atoi(parts[1])
	if err != nil || gotLimit != limit || parts[2] != kind {
		return 0, "", fmt.Errorf("%w: cursor: does not match this list", apiv1.ErrAskInvalid)
	}
	sec, err := strconv.ParseInt(parts[3], 10, 64)
	if err != nil || sec < 0 || !apiv1.ValidAskID(parts[4]) {
		return 0, "", fmt.Errorf("%w: cursor: malformed", apiv1.ErrAskInvalid)
	}
	return sec, parts[4], nil
}
