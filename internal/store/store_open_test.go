package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestOpenConfiguresSQLitePoolAndBusyTimeout(t *testing.T) {
	st := openTestStore(t, "open-config.sqlite")

	if got := st.DB.Stats().MaxOpenConnections; got != sqliteMaxOpenConns {
		t.Fatalf("unexpected max open conns: got %d want %d", got, sqliteMaxOpenConns)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	conn1, err := st.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("open first db conn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn1.Close()
	})

	conn2, err := st.DB.Conn(ctx)
	if err != nil {
		t.Fatalf("open second db conn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn2.Close()
	})

	checkConn := func(conn *sql.Conn, label string) {
		t.Helper()

		var busyTimeout int
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
			t.Fatalf("read busy_timeout on %s: %v", label, err)
		}
		if busyTimeout != int(sqliteBusyTimeout/time.Millisecond) {
			t.Fatalf("unexpected busy_timeout on %s: got %d want %d", label, busyTimeout, int(sqliteBusyTimeout/time.Millisecond))
		}

		var foreignKeys int
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
			t.Fatalf("read foreign_keys on %s: %v", label, err)
		}
		if foreignKeys != 1 {
			t.Fatalf("unexpected foreign_keys on %s: got %d want 1", label, foreignKeys)
		}
	}

	checkConn(conn1, "conn1")
	checkConn(conn2, "conn2")
}
