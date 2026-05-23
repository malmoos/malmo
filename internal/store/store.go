// Package store is the brain's SQLite persistence — the desired-state source
// of truth (CONTROL_PLANE.md, APP_LIFECYCLE.md). Skeleton scope: the app
// instances table only. Users, sessions, audit, etc. land later.
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// Instance is one installed app (a compose project, APP_LIFECYCLE.md).
type Instance struct {
	ID         string
	ManifestID string
	Name       string
	Slug       string
	Version    string
	State      string // installing | running | stopped | failed | uninstalling
	MDNSName   string
	CreatedAt  time.Time
}

var ErrNotFound = errors.New("instance not found")

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // sqlite: serialize writers, avoid "database is locked"
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`); err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS instances (
			id          TEXT PRIMARY KEY,
			manifest_id TEXT NOT NULL,
			name        TEXT NOT NULL,
			slug        TEXT NOT NULL UNIQUE,
			version     TEXT NOT NULL,
			state       TEXT NOT NULL,
			mdns_name   TEXT NOT NULL DEFAULT '',
			created_at  INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS instance_images (
			instance_id TEXT NOT NULL,
			service     TEXT NOT NULL,
			image       TEXT NOT NULL,
			digest      TEXT NOT NULL,
			PRIMARY KEY (instance_id, service),
			FOREIGN KEY (instance_id) REFERENCES instances(id) ON DELETE CASCADE
		);`)
	return err
}

// InstanceImage is the resolved image pin for one service in one instance
// (APP_LIFECYCLE.md # image digest pinning).
type InstanceImage struct {
	Service string
	Image   string // original `image:tag` reference from the author's compose
	Digest  string // `sha256:…`
}

// SetInstanceImages replaces the pinned images for an instance in one
// transaction. Called once per install (and later, per update) after digests
// have been resolved.
func (s *Store) SetInstanceImages(instanceID string, images []InstanceImage) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM instance_images WHERE instance_id=?`, instanceID); err != nil {
		return err
	}
	for _, img := range images {
		if _, err := tx.Exec(
			`INSERT INTO instance_images (instance_id, service, image, digest) VALUES (?,?,?,?)`,
			instanceID, img.Service, img.Image, img.Digest); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetInstanceImages returns the pinned images for an instance, ordered by
// service name.
func (s *Store) GetInstanceImages(instanceID string) ([]InstanceImage, error) {
	rows, err := s.db.Query(
		`SELECT service, image, digest FROM instance_images
		 WHERE instance_id=? ORDER BY service`, instanceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InstanceImage
	for rows.Next() {
		var i InstanceImage
		if err := rows.Scan(&i.Service, &i.Image, &i.Digest); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func (s *Store) Create(i Instance) error {
	_, err := s.db.Exec(
		`INSERT INTO instances (id, manifest_id, name, slug, version, state, mdns_name, created_at)
		 VALUES (?,?,?,?,?,?,?,?)`,
		i.ID, i.ManifestID, i.Name, i.Slug, i.Version, i.State, i.MDNSName, i.CreatedAt.Unix())
	return err
}

func (s *Store) SetState(id, state string) error {
	res, err := s.db.Exec(`UPDATE instances SET state=? WHERE id=?`, state, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) SetMDNSName(id, name string) error {
	_, err := s.db.Exec(`UPDATE instances SET mdns_name=? WHERE id=?`, name, id)
	return err
}

func (s *Store) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM instances WHERE id=?`, id)
	return err
}

func (s *Store) Get(id string) (Instance, error) {
	return scan(s.db.QueryRow(
		`SELECT id, manifest_id, name, slug, version, state, mdns_name, created_at
		 FROM instances WHERE id=?`, id))
}

func (s *Store) List() ([]Instance, error) {
	rows, err := s.db.Query(
		`SELECT id, manifest_id, name, slug, version, state, mdns_name, created_at
		 FROM instances ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Instance
	for rows.Next() {
		i, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// SlugTaken reports whether a slug is already used by an installed instance.
func (s *Store) SlugTaken(slug string) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM instances WHERE slug=?`, slug).Scan(&n)
	return n > 0, err
}

type scanner interface{ Scan(dest ...any) error }

func scan(row scanner) (Instance, error) {
	var i Instance
	var created int64
	err := row.Scan(&i.ID, &i.ManifestID, &i.Name, &i.Slug, &i.Version, &i.State, &i.MDNSName, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return Instance{}, ErrNotFound
	}
	if err != nil {
		return Instance{}, fmt.Errorf("scan instance: %w", err)
	}
	i.CreatedAt = time.Unix(created, 0)
	return i, nil
}
