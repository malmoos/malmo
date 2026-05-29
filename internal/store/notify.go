package store

import (
	"database/sql"
	"time"

	"github.com/malmo/malmo/internal/notify"
)

// RaiseNotification coalesces by dedup_key (NOTIFICATIONS.md # One notification
// per raise): if an active (non-dismissed) row for the same key exists it is
// refreshed in place — ts/severity/summary/body/action bumped and resolved_at
// cleared — so a flapping issue never produces a second row. Otherwise a new
// row is inserted. Writers are serialized (SetMaxOpenConns(1)), so the
// update-then-insert is race-free; the partial unique index on
// (dedup_key) WHERE dismissed_at IS NULL is the backstop.
func (s *Store) RaiseNotification(n notify.Notification) error {
	res, err := s.db.Exec(
		`UPDATE notifications
		   SET ts=?, severity=?, summary=?, body=?, action_label=?, action_route=?,
		       resolved_at=NULL
		 WHERE dedup_key=? AND dismissed_at IS NULL`,
		n.TS, string(n.Severity), n.Summary, n.Body, n.ActionLabel, n.ActionRoute,
		n.DedupKey)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows > 0 {
		return nil
	}

	var userID any
	if n.UserID != "" {
		userID = n.UserID
	}
	_, err = s.db.Exec(
		`INSERT INTO notifications
		  (ts, category, severity, source_kind, source_id, dedup_key,
		   audience, user_id, variant, summary, body, action_label, action_route)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.TS, string(n.Category), string(n.Severity), n.SourceKind, n.SourceID, n.DedupKey,
		n.Audience, userID, n.Variant, n.Summary, n.Body, n.ActionLabel, n.ActionRoute)
	return err
}

// ResolveNotification marks the active notification for dedupKey resolved
// (NOTIFICATIONS.md # Clears) — the original notification stays on the timeline,
// marked resolved rather than deleted. No-op when no active row matches, so a
// clear of an issue that never notified is harmless.
func (s *Store) ResolveNotification(dedupKey string, at time.Time) error {
	_, err := s.db.Exec(
		`UPDATE notifications SET resolved_at=?
		 WHERE dedup_key=? AND dismissed_at IS NULL`,
		at.UnixMilli(), dedupKey)
	return err
}

// ListNotifications returns notifications newest-first. Skeleton scope for the
// write seam: no filtering yet — the audience/read-state-aware list the bell
// API needs lands with that slice. Used by tests today.
func (s *Store) ListNotifications() ([]notify.Notification, error) {
	rows, err := s.db.Query(
		`SELECT id, ts, category, severity, source_kind, source_id, dedup_key,
		        audience, user_id, variant, summary, body, action_label, action_route,
		        resolved_at
		 FROM notifications ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []notify.Notification
	for rows.Next() {
		var n notify.Notification
		var category, severity string
		var userID sql.NullString
		var resolvedAt sql.NullInt64
		if err := rows.Scan(
			&n.ID, &n.TS, &category, &severity, &n.SourceKind, &n.SourceID, &n.DedupKey,
			&n.Audience, &userID, &n.Variant, &n.Summary, &n.Body, &n.ActionLabel, &n.ActionRoute,
			&resolvedAt,
		); err != nil {
			return nil, err
		}
		n.Category = notify.Category(category)
		n.Severity = notify.Severity(severity)
		n.UserID = userID.String
		n.ResolvedAt = resolvedAt.Int64
		out = append(out, n)
	}
	return out, rows.Err()
}
