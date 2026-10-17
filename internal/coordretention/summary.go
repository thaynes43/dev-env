// Package coordretention lists coordination records, decides release rules, and
// runs the leader-only retention pass from DESIGN-024 section 5. The runnable
// is default off: the operator starts it only with --enable-coordination-retention,
// which itself stays off unless a deployment sets it. Deletes go through release.
package coordretention

import (
	"context"
	"fmt"
	"sort"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	coord "github.com/thaynes43/dev-env/api/coordination/v1alpha1"
	"github.com/thaynes43/dev-env/internal/agentd/protocol"
	"github.com/thaynes43/dev-env/internal/contextpublication"
	"github.com/thaynes43/dev-env/internal/inbox"
	"github.com/thaynes43/dev-env/internal/taskclaim"
)

// Kind is one retention-supported coordination resource.
type Kind string

const (
	TaskClaim        Kind = "TaskClaim"
	PublishedContext Kind = "PublishedContext"
	InboxMessage     Kind = "InboxMessage"
	InboxReadAck     Kind = "InboxReadAck"
)

// SessionRef contains only the immutable identity needed for an absence proof.
type SessionRef struct{ Namespace, Name, UID string }

// MessageRef identifies a live message for the later, bulk archive lookup.
type MessageRef struct{ Namespace, Project, Name, UID string }

// Summary deliberately retains neither record text nor an object/deep copy.
type Summary struct {
	Kind                                                     Kind
	Resource, Project, Namespace, Name, UID, ResourceVersion string
	Owner, Source, Sender, Recipient                         SessionRef
	Message                                                  MessageRef
	Valid                                                    bool
}

// ListConfig supplies the uncached API reader and the explicitly enabled kinds.
// Reader must be the operator APIReader; this package never substitutes a cache.
type ListConfig struct {
	Reader    client.Reader
	Namespace string
	Enabled   map[Kind]bool
}

type resource struct {
	kind Kind
	name string
	page int64
	gvk  schema.GroupVersionKind
}

var resources = []resource{
	{TaskClaim, "taskclaims", 50, coord.GroupVersion.WithKind("TaskClaim")},
	{PublishedContext, "publishedcontexts", 10, coord.GroupVersion.WithKind("PublishedContext")},
	{InboxMessage, "inboxmessages", 10, coord.GroupVersion.WithKind("InboxMessage")},
	{InboxReadAck, "inboxreadacks", 50, coord.GroupVersion.WithKind("InboxReadAck")},
}

// Summaries lists enabled resources only in the supplied operator namespace.
// Each List is bounded and terminates if a server repeats a continuation cursor.
func Summaries(ctx context.Context, c ListConfig) ([]Summary, error) {
	if c.Reader == nil || len(validation.IsDNS1123Label(c.Namespace)) != 0 {
		return nil, fmt.Errorf("coordination retention summaries: missing uncached reader or namespace")
	}
	var out []Summary
	for _, r := range resources {
		if !c.Enabled[r.kind] {
			continue
		}
		cursor, seen := "", map[string]struct{}{}
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			list := &unstructured.UnstructuredList{}
			list.SetGroupVersionKind(r.gvk.GroupVersion().WithKind(r.gvk.Kind + "List"))
			err := c.Reader.List(ctx, list, client.InNamespace(c.Namespace), client.Limit(r.page), client.Continue(cursor))
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			if err != nil {
				return nil, fmt.Errorf("list %s: %w", r.kind, err)
			}
			if int64(len(list.Items)) > r.page {
				return nil, fmt.Errorf("list %s: page exceeds requested bound", r.kind)
			}
			for i := range list.Items {
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				out = append(out, summarize(r, c.Namespace, &list.Items[i]))
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			next := list.GetContinue()
			if next == "" {
				break
			}
			if _, ok := seen[next]; ok || next == cursor {
				return nil, fmt.Errorf("list %s: repeated continuation cursor", r.kind)
			}
			seen[next] = struct{}{}
			cursor = next
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.Project != b.Project {
			return a.Project < b.Project
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.UID < b.UID
	})
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func ref(s coord.SessionIdentity) SessionRef { return SessionRef{s.Namespace, s.Name, s.UID} }
func objectSummary(r resource, ns string, u *unstructured.Unstructured) Summary {
	return Summary{Kind: r.kind, Resource: r.name, Namespace: ns, Name: u.GetName(), UID: string(u.GetUID()), ResourceVersion: u.GetResourceVersion()}
}
func validMeta(s Summary, namespace string) bool {
	if s.Namespace != namespace || len(validation.IsDNS1123Label(namespace)) != 0 {
		return false
	}
	expected := ""
	for _, r := range resources {
		if r.kind == s.Kind {
			expected = r.name
			break
		}
	}
	if expected == "" || s.Resource != expected {
		return false
	}
	if _, err := (protocol.CoordArchiveIdentity{Resource: s.Resource, Project: s.Project, Name: s.Name, UID: s.UID}).RecordDir(); err != nil {
		return false
	}
	return (protocol.CoordArchiveExactQuery{Resource: s.Resource, Name: s.Name, UID: s.UID, ResourceVersion: s.ResourceVersion, SHA256: "0000000000000000000000000000000000000000000000000000000000000000"}).Validate() == nil
}
func summarize(r resource, namespace string, u *unstructured.Unstructured) Summary {
	s := objectSummary(r, namespace, u)
	if u.GetNamespace() != namespace {
		return s
	}
	switch r.kind {
	case TaskClaim:
		var v coord.TaskClaim
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &v) == nil {
			s.Project, s.Owner = v.Spec.Identity.Project, ref(v.Spec.Owner)
			s.Valid = validMeta(s, namespace) && taskclaim.ValidSpec(v.Spec) && u.GetName() == taskclaim.Name(v.Spec.Identity)
		}
	case PublishedContext:
		var v coord.PublishedContext
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &v) == nil {
			s.Project, s.Source = v.Spec.Project, ref(v.Spec.Source)
			s.Valid = validMeta(s, namespace) && contextpublication.ValidSpec(v.Spec) && u.GetName() == contextpublication.Name(v.Spec.Identity)
		}
	case InboxMessage:
		var v coord.InboxMessage
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &v) == nil {
			s.Project, s.Sender, s.Recipient = v.Spec.Project, ref(v.Spec.Sender), ref(v.Spec.Recipient)
			s.Valid = validMeta(s, namespace) && inbox.ValidMessage(v.Spec) && u.GetName() == inbox.MessageName(v.Spec.Sender, v.Spec.Key)
		}
	case InboxReadAck:
		var v coord.InboxReadAck
		if runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, &v) == nil {
			s.Project, s.Recipient = v.Spec.Project, ref(v.Spec.Recipient)
			s.Message = MessageRef{Namespace: namespace, Project: v.Spec.Project, Name: v.Spec.MessageName, UID: v.Spec.MessageUID}
			s.Valid = validMeta(s, namespace) && inbox.ValidAck(v.Spec) && u.GetName() == inbox.AckName(v.Spec.MessageUID, v.Spec.Recipient.UID)
		}
	}
	return s
}
