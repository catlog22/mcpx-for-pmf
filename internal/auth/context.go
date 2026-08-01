package auth

import "context"

type ctxKey int

const authorizationKey ctxKey = 1

// ContextWithAuthorization stores the raw Authorization header value.
func ContextWithAuthorization(ctx context.Context, header string) context.Context {
	if header == "" {
		return ctx
	}
	return context.WithValue(ctx, authorizationKey, header)
}

// AuthorizationFromContext returns the Authorization header if present.
func AuthorizationFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	v, _ := ctx.Value(authorizationKey).(string)
	return v
}
