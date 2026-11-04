package executor

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/thaynes43/dev-env/internal/scheduledwork/pages"
)

// NewPager returns the executor's page Reminder creator. It creates through
// c, the operator's writing client, and makes its one read (the existing
// Reminder after AlreadyExists) through r, the uncached API reader, so it
// starts no informer on Reminders.
func NewPager(c client.Client, r client.Reader) *pages.Pager {
	return &pages.Pager{Client: uncachedReads{Client: c, reader: r}}
}

// uncachedReads is a client whose reads go to reader and whose writes go to
// the embedded Client.
type uncachedReads struct {
	client.Client
	reader client.Reader
}

func (c uncachedReads) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	return c.reader.Get(ctx, key, obj, opts...)
}

func (c uncachedReads) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	return c.reader.List(ctx, list, opts...)
}
