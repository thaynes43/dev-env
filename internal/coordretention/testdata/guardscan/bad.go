package guardscan

import "context"

func extraDelete(c interface {
	Delete(context.Context, any, ...any) error
}, ctx context.Context, obj any) error {
	return c.Delete(ctx, obj)
}

func deleteAll(c interface {
	DeleteAllOf(context.Context, any) error
}, ctx context.Context, obj any) error {
	return c.DeleteAllOf(ctx, obj)
}

func deleteRecord(c interface {
	Delete(context.Context, any, ...any) error
}, ctx context.Context, obj any, uid *string) error {
	if err := c.Delete(ctx, obj); err != nil {
		return err
	}
	return c.Delete(ctx, obj, struct{ UID *string }{UID: uid})
}

func release(ctx context.Context) error {
	releaseRechecked(ctx)
	return deleteRecord(nil, ctx, nil, nil)
}

func sneaky(ctx context.Context) error {
	return deleteRecord(nil, ctx, nil, nil)
}

func releaseRechecked(context.Context) {}
