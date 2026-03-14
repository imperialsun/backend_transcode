package store

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
)

func TestNormalizeStatus(t *testing.T) {
	if normalizeStatus("inactive") != "inactive" {
		t.Fatal("expected inactive")
	}
	if normalizeStatus("DISABLED") != "inactive" {
		t.Fatal("expected inactive for disabled")
	}
	if normalizeStatus("  whatever  ") != "active" {
		t.Fatal("expected active for other values")
	}
}

func TestNormalizeOrgCode(t *testing.T) {
	if normalizeOrgCode(" My Org ") != "my-org" {
		t.Fatal("expected normalized org code")
	}
	if normalizeOrgCode("") == "" {
		t.Fatal("expected non-empty default code")
	}
}

func TestUniqueNormalizedCodes(t *testing.T) {
	out := uniqueNormalizedCodes([]string{"a", "b", "a", " "})
	if len(out) != 2 {
		t.Fatalf("expected 2 unique values, got %v", out)
	}
}

func TestSortStrings(t *testing.T) {
	vals := []string{"z", "a", "m"}
	sortStrings(vals)
	if strings.Join(vals, ",") != "a,m,z" {
		t.Fatalf("unexpected sorted order: %v", vals)
	}
}

func TestIsActivityEventDuplicateErr(t *testing.T) {
	if isActivityEventDuplicateErr(nil) {
		t.Fatal("expected false for nil error")
	}
	if isActivityEventDuplicateErr(errors.New("something else")) {
		t.Fatal("expected false for unrelated error")
	}
	err := errors.New("UNIQUE constraint failed: activity_usage_events.event_id")
	if !isActivityEventDuplicateErr(err) {
		t.Fatal("expected true for duplicate event error")
	}
}

func TestScanStringRowsAndCatalogRows(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	defer db.Close()
	_, _ = db.Exec(`CREATE TABLE foo (value TEXT); INSERT INTO foo(value) VALUES('x'),('y')`)
	rows, err := db.Query(`SELECT value FROM foo ORDER BY value ASC`)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	vals, err := scanStringRows(rows)
	if err != nil {
		t.Fatalf("scanStringRows failed: %v", err)
	}
	if len(vals) != 2 || vals[0] != "x" {
		t.Fatalf("unexpected values: %v", vals)
	}

	_, _ = db.Exec(`CREATE TABLE bar (code TEXT, label TEXT); INSERT INTO bar(code,label) VALUES('c1','L1')`)
	rows2, err := db.Query(`SELECT code, label FROM bar`)
	if err != nil {
		t.Fatalf("query failed: %v", err)
	}
	cats, err := scanCatalogRows(rows2)
	if err != nil {
		t.Fatalf("scanCatalogRows failed: %v", err)
	}
	if len(cats) != 1 || cats[0]["code"] != "c1" {
		t.Fatalf("unexpected catalog rows: %v", cats)
	}
}
