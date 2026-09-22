package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

func TestSessionStoreRoundtrip(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "sessions_test.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	st, err := newSessionStore(ctx, db)
	if err != nil {
		t.Fatal(err)
	}

	id := newSessionID()
	if len(id) != 32 {
		t.Fatalf("session id should be 32 hex chars, got %d", len(id))
	}
	if err := st.insert(ctx, id, "Account A"); err != nil {
		t.Fatal(err)
	}

	rows, err := st.list(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != id || rows[0].Name != "Account A" || rows[0].JID != "" {
		t.Fatalf("unexpected rows after insert: %+v", rows)
	}

	if err := st.setJID(ctx, id, "5511999999999:1@s.whatsapp.net"); err != nil {
		t.Fatal(err)
	}
	rows, _ = st.list(ctx)
	if rows[0].JID != "5511999999999:1@s.whatsapp.net" {
		t.Fatalf("jid not persisted: %+v", rows[0])
	}

	if err := st.delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	rows, _ = st.list(ctx)
	if len(rows) != 0 {
		t.Fatalf("expected empty after delete, got %+v", rows)
	}
}

func TestCallHistoryRoundtrip(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t, "")

	ended := int64(1_700_000_005_000)
	owner := "op-A"
	in := CallRecord{
		SessionID: "s1", CallID: "C1", Owner: &owner, Direction: "outbound",
		Peer: "5511999@s.whatsapp.net", StartedAt: 1_700_000_000_000,
		EndedAt: &ended, EndReason: "user_ended", Recording: "1700000000000-C1",
	}
	if err := st.appendCall(ctx, in); err != nil {
		t.Fatal(err)
	}
	// A second call, unrecorded and in another session, to prove the filtering.
	if err := st.appendCall(ctx, CallRecord{
		SessionID: "s2", CallID: "C2", Direction: "inbound", Peer: "x", EndReason: "declined",
	}); err != nil {
		t.Fatal(err)
	}

	got, err := st.recentCalls(ctx, "s1", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 row for s1, got %d", len(got))
	}
	r := got[0]
	if r.CallID != "C1" || r.Recording != "1700000000000-C1" || r.Status != StatusEnded {
		t.Fatalf("row did not round-trip: %+v", r)
	}
	if r.Owner == nil || *r.Owner != owner {
		t.Fatalf("owner did not round-trip: %+v", r.Owner)
	}
	if r.EndedAt == nil || *r.EndedAt != ended {
		t.Fatalf("endedAt did not round-trip: %+v", r.EndedAt)
	}

	all, err := st.recentCalls(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 || all[0].CallID != "C2" {
		t.Fatalf("empty session should return every row, newest first: %+v", all)
	}

	base, err := st.recordingBase(ctx, "s1", "C1")
	if err != nil || base != "1700000000000-C1" {
		t.Fatalf("recordingBase = %q, %v", base, err)
	}
	if base, _ := st.recordingBase(ctx, "s2", "C2"); base != "" {
		t.Fatalf("unrecorded call should have an empty base, got %q", base)
	}
}

// The call_history table shipped before the recording column existed, so opening
// an older database has to add the column rather than leave the table as it is —
// CREATE TABLE IF NOT EXISTS does nothing once the table is there.
func TestCallHistoryMigratesOlderDatabase(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Exactly the schema of the previous release: no recording column.
	if _, err := db.ExecContext(ctx, `CREATE TABLE call_history (
		rowid_alias INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id  TEXT NOT NULL,
		call_id     TEXT NOT NULL,
		owner       TEXT,
		direction   TEXT NOT NULL,
		peer        TEXT NOT NULL,
		started_at  INTEGER NOT NULL,
		ended_at    INTEGER NOT NULL,
		end_reason  TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx,
		`INSERT INTO call_history (session_id, call_id, direction, peer, started_at, ended_at, end_reason)
		 VALUES ('s1', 'OLD', 'outbound', 'x', 1, 2, 'user_ended')`); err != nil {
		t.Fatal(err)
	}

	st, err := newSessionStore(ctx, db)
	if err != nil {
		t.Fatalf("opening an older database should migrate it, got %v", err)
	}

	// The pre-existing row survives, with an empty recording.
	rows, err := st.recentCalls(ctx, "s1", 10)
	if err != nil {
		t.Fatalf("reading migrated table: %v", err)
	}
	if len(rows) != 1 || rows[0].CallID != "OLD" || rows[0].Recording != "" {
		t.Fatalf("old row did not survive the migration: %+v", rows)
	}
	// And new rows can use the column.
	if err := st.appendCall(ctx, CallRecord{SessionID: "s1", CallID: "NEW", Direction: "inbound", Peer: "y", Recording: "base-NEW"}); err != nil {
		t.Fatal(err)
	}
	if base, _ := st.recordingBase(ctx, "s1", "NEW"); base != "base-NEW" {
		t.Fatalf("recording column unusable after migration, got %q", base)
	}

	// Re-opening must be a no-op, not a duplicate-column error.
	if _, err := newSessionStore(ctx, db); err != nil {
		t.Fatalf("second open should be idempotent, got %v", err)
	}
}

func openTestStore(t *testing.T, name string) *sessionStore {
	t.Helper()
	if name == "" {
		name = "store_test.db"
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	st, err := newSessionStore(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	return st
}
