package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
)

type sessionRow struct {
	ID   string
	Name string
	JID  string
}

type sessionStore struct{ db *sql.DB }

var _ historyStore = (*sessionStore)(nil)

func newSessionStore(ctx context.Context, db *sql.DB) (*sessionStore, error) {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS sessions (
		id   TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		jid  TEXT
	)`)
	if err != nil {
		return nil, err
	}
	// Call history used to live only in a slice that grew for the lifetime of the
	// process and vanished on restart.
	_, err = db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS call_history (
		rowid_alias INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id  TEXT NOT NULL,
		call_id     TEXT NOT NULL,
		owner       TEXT,
		direction   TEXT NOT NULL,
		peer        TEXT NOT NULL,
		started_at  INTEGER NOT NULL,
		ended_at    INTEGER NOT NULL,
		end_reason  TEXT NOT NULL
	)`)
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(ctx,
		`CREATE INDEX IF NOT EXISTS idx_call_history_session ON call_history (session_id, rowid_alias DESC)`); err != nil {
		return nil, err
	}
	if err := addColumnIfMissing(ctx, db, "call_history", "recording", "TEXT"); err != nil {
		return nil, err
	}
	return &sessionStore{db: db}, nil
}

// addColumnIfMissing extends an existing table. CREATE TABLE IF NOT EXISTS does
// nothing once the table is there, so a column added after the first release
// needs its own ALTER — and that ALTER has to be a no-op on databases that
// already have it.
func addColumnIfMissing(ctx context.Context, db *sql.DB, table, column, decl string) error {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `ALTER TABLE `+table+` ADD COLUMN `+column+` `+decl)
	return err
}

func newSessionID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *sessionStore) list(ctx context.Context) ([]sessionRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, COALESCE(jid, '') FROM sessions ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []sessionRow
	for rows.Next() {
		var r sessionRow
		if err := rows.Scan(&r.ID, &r.Name, &r.JID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *sessionStore) insert(ctx context.Context, id, name string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id, name, jid) VALUES (?, ?, NULL)`, id, name)
	return err
}

func (s *sessionStore) setJID(ctx context.Context, id, jid string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE sessions SET jid = ? WHERE id = ?`, jid, id)
	return err
}

func (s *sessionStore) delete(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// appendCall records one ended call. It satisfies broker.historyStore.
func (s *sessionStore) appendCall(ctx context.Context, r CallRecord) error {
	var owner any
	if r.Owner != nil {
		owner = *r.Owner
	}
	var endedAt int64
	if r.EndedAt != nil {
		endedAt = *r.EndedAt
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO call_history (session_id, call_id, owner, direction, peer, started_at, ended_at, end_reason, recording)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.SessionID, r.CallID, owner, r.Direction, r.Peer, r.StartedAt, endedAt, r.EndReason, r.Recording)
	return err
}

// recordingBase returns the recording file stem stored for a finished call, or
// "" when that call has no recording.
func (s *sessionStore) recordingBase(ctx context.Context, sessionID, callID string) (string, error) {
	var base sql.NullString
	err := s.db.QueryRowContext(ctx,
		`SELECT recording FROM call_history
		 WHERE session_id = ? AND call_id = ?
		 ORDER BY rowid_alias DESC LIMIT 1`, sessionID, callID).Scan(&base)
	if err != nil {
		return "", err
	}
	return base.String, nil
}

// recentCalls returns ended calls newest first. An empty sessionID means all
// sessions.
func (s *sessionStore) recentCalls(ctx context.Context, sessionID string, limit int) ([]CallRecord, error) {
	query := `SELECT session_id, call_id, owner, direction, peer, started_at, ended_at, end_reason, recording
	          FROM call_history`
	args := []any{}
	if sessionID != "" {
		query += ` WHERE session_id = ?`
		args = append(args, sessionID)
	}
	query += ` ORDER BY rowid_alias DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]CallRecord, 0, limit)
	for rows.Next() {
		var r CallRecord
		var owner, recording sql.NullString
		var endedAt int64
		if err := rows.Scan(&r.SessionID, &r.CallID, &owner, &r.Direction, &r.Peer,
			&r.StartedAt, &endedAt, &r.EndReason, &recording); err != nil {
			return nil, err
		}
		if owner.Valid {
			r.Owner = &owner.String
		}
		r.Recording = recording.String
		r.EndedAt = &endedAt
		r.Status = StatusEnded
		out = append(out, r)
	}
	return out, rows.Err()
}
