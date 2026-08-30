package auth

import (
	"context"
)

type ctxKey int

const ctxIdentity ctxKey = iota

// WithIdentity attaches an authenticated identity to a request context.
func WithIdentity(ctx context.Context, id *Identity) context.Context {
	return context.WithValue(ctx, ctxIdentity, id)
}

// IdentityFrom returns the authenticated identity attached by middleware, or
// nil when the request is anonymous.
func IdentityFrom(ctx context.Context) *Identity {
	id, _ := ctx.Value(ctxIdentity).(*Identity)
	return id
}

// HasPermission reports whether the identity holds a permission code.
func (i *Identity) HasPermission(code string) bool {
	for _, p := range i.Permissions {
		if p == code {
			return true
		}
	}
	return false
}
