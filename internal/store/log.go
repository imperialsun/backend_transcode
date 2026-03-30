package store

import (
	"context"
	"log"

	"demeter-backend/internal/backenderrors"
	"demeter-backend/internal/observability"
)

const storeLogRoute = "sqlite"

func logStoreStep(ctx context.Context, step, title string, fields map[string]any) {
	log.Print(observability.FormatStepLine("store", storeLogRoute, step, observability.TraceIDFromContext(ctx), observability.DefaultTraceID, observability.DefaultTraceID, title, fields))
	backenderrors.RecordLog(ctx, "store", storeLogRoute, step, title, fields)
}
