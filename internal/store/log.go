package store

import (
	"context"
	"log"

	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/observability"
	"demeter-backend/internal/requestmeta"
)

const storeLogRoute = "sqlite"

// logStoreStep emits a structured store event and forwards it into the backend
// error pipeline so persistence problems stay observable.
func logStoreStep(ctx context.Context, step, title string, fields map[string]any) {
	userID, orgID := observability.DefaultTraceID, observability.DefaultTraceID
	if actorUserID, actorOrgID, ok := requestmeta.ActorFromContext(ctx); ok {
		if actorUserID != "" {
			userID = actorUserID
		}
		if actorOrgID != "" {
			orgID = actorOrgID
		}
	}
	log.Print(observability.FormatStepLine("store", storeLogRoute, step, observability.TraceIDFromContext(ctx), userID, orgID, title, fields))
	backenderrors.RecordLog(ctx, "store", storeLogRoute, step, title, fields)
}
