package store

import (
	"context"
	"errors"
	"time"

	moderncsqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

const (
	sqliteRetryAttempts = 3
	sqliteRetryDelay    = 25 * time.Millisecond
)

// SQLite can surface SQLITE_BUSY_SNAPSHOT when a deferred transaction reads and
// then tries to write after a concurrent commit. Retrying the full idempotent
// operation is safer than failing the request immediately.
func withSQLiteRetry[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T

	for attempt := 0; attempt < sqliteRetryAttempts; attempt++ {
		result, err := fn()
		if err == nil {
			return result, nil
		}
		if !isRetryableSQLiteError(err) || attempt == sqliteRetryAttempts-1 {
			return zero, err
		}

		delay := sqliteRetryDelay << attempt
		if ctx == nil {
			time.Sleep(delay)
			continue
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}

	return zero, context.Canceled
}

func isRetryableSQLiteError(err error) bool {
	var sqliteErr *moderncsqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}

	switch sqliteErr.Code() & 0xFF {
	case sqlite3.SQLITE_BUSY, sqlite3.SQLITE_LOCKED:
		return true
	default:
		return false
	}
}
