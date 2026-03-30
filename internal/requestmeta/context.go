package requestmeta

import "context"

type actorContextKey struct{}

type Actor struct {
	UserID string
	OrgID  string
}

func WithActor(ctx context.Context, userID, orgID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, actorContextKey{}, Actor{
		UserID: userID,
		OrgID:  orgID,
	})
}

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
