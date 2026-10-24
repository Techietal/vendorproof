package httpserver

import (
	"context"

	"vendorproof/internal/identity"
)

type identityKey struct{}

func withIdentity(ctx context.Context, ident identity.Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, ident)
}

func identityFrom(ctx context.Context) (identity.Identity, bool) {
	ident, ok := ctx.Value(identityKey{}).(identity.Identity)
	return ident, ok && ident.Valid()
}
