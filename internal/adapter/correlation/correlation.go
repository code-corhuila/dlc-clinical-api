// Package correlation carries the request's X-Correlation-Id through the
// context, from the inbound adapter to every outbound call (Norma 5.3.9).
package correlation

import "context"

type key struct{}

// WithID returns a context carrying the correlation id.
func WithID(ctx context.Context, id string) context.Context { return context.WithValue(ctx, key{}, id) }

// ID returns the correlation id of the context, or "" when none was set.
func ID(ctx context.Context) string {
	id, _ := ctx.Value(key{}).(string)
	return id
}
