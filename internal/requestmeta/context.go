package requestmeta

import "context"

type actorContextKey struct{}

// Actor stores the authenticated user and organization that should be attached
// to downstream logs and persistence records.
type Actor struct {
	UserID string
	OrgID  string
}

// WithActor stores actor metadata in a context, creating a background context
// first when the caller passes nil.
func WithActor(ctx context.Context, userID, orgID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, actorContextKey{}, Actor{
		UserID: userID,
		OrgID:  orgID,
	})
}

// ActorFromContext extracts the stored actor metadata when present.
func ActorFromContext(ctx context.Context) (string, string, bool) {
	if ctx == nil {
		return "", "", false
	}
	actor, ok := ctx.Value(actorContextKey{}).(Actor)
	if !ok {
		return "", "", false
	}
	return actor.UserID, actor.OrgID, true
}
