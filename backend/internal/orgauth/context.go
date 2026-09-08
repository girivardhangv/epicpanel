package orgauth

import (
	"context"

	"github.com/google/uuid"
)

func contextWithValue(ctx context.Context, key, val any) context.Context {
	return context.WithValue(ctx, key, val)
}

func contextValue(ctx context.Context, key any) any {
	return ctx.Value(key)
}

func withOrg(ctx context.Context, id uuid.UUID) context.Context {
	return contextWithValue(ctx, orgKey{}, id)
}
