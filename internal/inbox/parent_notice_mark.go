package inbox

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
)

// ParentNoticeAnnotation is the sent mark for the wake notice (ADR-023).
// The value is the time the operator was about to deliver, RFC3339Nano.
const ParentNoticeAnnotation = "dev-env.haynesops.com/parent-notice"

const (
	noticePage    = 100
	noticeMax     = 512
	noticeScanMax = 4096
)

// NoticeKind is a child note that can wake a session parent: question, result
// or blocker. A reply and a plain message do not.
func NoticeKind(kind string) bool {
	return kind == KindQuestion || kind == KindResult || kind == KindBlocker
}

// annotationsOK allows no annotations, or exactly the parent-notice mark on a
// parent note. Any other annotation, including on a peer message or an ack,
// stays invalid so a marked note can still be read and a foreign mark cannot.
func annotationsOK(o client.Object) bool {
	ann := o.GetAnnotations()
	if len(ann) == 0 {
		return true
	}
	msg, ok := o.(*coord.InboxMessage)
	return ok && msg != nil && IsParentNote(msg) && parentNoticeAnnotationsOK(ann)
}

func parentNoticeAnnotationsOK(ann map[string]string) bool {
	if len(ann) != 1 {
		return false
	}
	v, ok := ann[ParentNoticeAnnotation]
	if !ok || v == "" || len(v) > 64 || strings.ContainsAny(v, "\r\n") {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, v)
	return err == nil && !t.IsZero()
}

// MarkParentNotice writes the sent mark. The patch is metadata.annotations
// plus the note's resourceVersion as the precondition. The spec is not in the
// patch. A refused patch, including a lost precondition, is returned as the
// API error.
func (s KubeStore) MarkParentNotice(ctx context.Context, note *coord.InboxMessage, at time.Time) error {
	if !s.ready() || note == nil || note.Namespace != s.Namespace || !ValidMessageName(note.Name) || note.ResourceVersion == "" || at.IsZero() {
		return ErrUnavailable
	}
	body, err := json.Marshal(parentNoticePatchBody{
		Metadata: parentNoticePatchMeta{
			ResourceVersion: note.ResourceVersion,
			Annotations:     map[string]string{ParentNoticeAnnotation: at.UTC().Format(time.RFC3339Nano)},
		},
	})
	if err != nil {
		return ErrUnavailable
	}
	return s.Client.Patch(ctx, note, client.RawPatch(types.MergePatchType, body))
}

type parentNoticePatchBody struct {
	Metadata parentNoticePatchMeta `json:"metadata"`
}

type parentNoticePatchMeta struct {
	Annotations     map[string]string `json:"annotations"`
	ResourceVersion string            `json:"resourceVersion"`
}

// NoticeNotes lists parent notes addressed to one session, newest page after
// page, including marked notes. An incomplete or mismatched page is refused
// so a pass does not deliver from a partial read.
func (s KubeStore) NoticeNotes(ctx context.Context, party coord.SessionIdentity) ([]coord.InboxMessage, error) {
	if !s.ready() || !ValidSession(party) {
		return nil, ErrUnavailable
	}
	sel := labels.SelectorFromSet(map[string]string{
		ParentNoteLabel: "true",
		RecipientLabel:  recipientLabel(party),
	})
	var out []coord.InboxMessage
	var cont string
	for {
		var page coord.InboxMessageList
		if s.Live.List(ctx, &page, client.InNamespace(s.Namespace), client.MatchingLabelsSelector{Selector: sel}, client.Limit(noticePage), client.Continue(cont)) != nil {
			return nil, ErrUnavailable
		}
		if int64(len(page.Items)) > noticePage {
			return nil, ErrUnavailable
		}
		for i := range page.Items {
			m := &page.Items[i]
			if !IsParentNote(m) || m.Spec.Recipient != party || !m.GetDeletionTimestamp().IsZero() || !s.validMessage(m) {
				return nil, ErrUnavailable
			}
			out = append(out, *m)
		}
		if len(out) > noticeMax {
			return nil, ErrUnavailable
		}
		if page.Continue == "" {
			return out, nil
		}
		if len(page.Continue) > 4096 {
			return nil, ErrUnavailable
		}
		cont = page.Continue
	}
}

// NoticeParents returns session recipients that have at least one unmarked
// question, result or blocker. A caller recipient is not included. A list
// that does not finish is refused.
func (s KubeStore) NoticeParents(ctx context.Context) ([]coord.SessionIdentity, error) {
	if !s.ready() {
		return nil, ErrUnavailable
	}
	sel := labels.SelectorFromSet(map[string]string{ParentNoteLabel: "true"})
	seen := map[coord.SessionIdentity]struct{}{}
	var out []coord.SessionIdentity
	var cont string
	n := 0
	for {
		var page coord.InboxMessageList
		if s.Live.List(ctx, &page, client.InNamespace(s.Namespace), client.MatchingLabelsSelector{Selector: sel}, client.Limit(noticePage), client.Continue(cont)) != nil {
			return nil, ErrUnavailable
		}
		n += len(page.Items)
		if n > noticeScanMax || int64(len(page.Items)) > noticePage {
			return nil, ErrUnavailable
		}
		for i := range page.Items {
			m := &page.Items[i]
			if !IsParentNote(m) || !m.GetDeletionTimestamp().IsZero() {
				return nil, ErrUnavailable
			}
			if m.Spec.CallerRecipient != nil || m.Spec.Recipient == (coord.SessionIdentity{}) || !NoticeKind(m.Spec.Kind) {
				continue
			}
			if _, marked := m.Annotations[ParentNoticeAnnotation]; marked {
				continue
			}
			if _, ok := seen[m.Spec.Recipient]; ok {
				continue
			}
			seen[m.Spec.Recipient] = struct{}{}
			out = append(out, m.Spec.Recipient)
		}
		if page.Continue == "" {
			return out, nil
		}
		if len(page.Continue) > 4096 {
			return nil, ErrUnavailable
		}
		cont = page.Continue
	}
}
