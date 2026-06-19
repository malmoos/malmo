package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// ResourceLimits is the per-instance cgroup limit policy for an app's main
// service (ENVIRONMENT.md # Per-instance resource limits). The zero value means
// "no limit" — the app bursts freely (APP_ISOLATION.md # Resource limits).
// MemoryBytes maps to the container's HostConfig.Memory; NanoCPUs to NanoCpus (a
// whole CPU is 1e9). A zero field leaves that dimension uncapped.
type ResourceLimits struct {
	MemoryBytes int64
	NanoCPUs    int64
}

// IsZero reports whether no limit is set in either dimension.
func (r ResourceLimits) IsZero() bool { return r.MemoryBytes == 0 && r.NanoCPUs == 0 }

// GetResourceLimits returns the persisted limit policy for an instance. A
// missing row is not an error — it returns the zero value (uncapped), the common
// case for an app no one has clamped.
func (s *Store) GetResourceLimits(instanceID string) (ResourceLimits, error) {
	var rl ResourceLimits
	err := s.db.QueryRow(
		`SELECT memory_bytes, nano_cpus FROM instance_resource_limits WHERE instance_id=?`,
		instanceID,
	).Scan(&rl.MemoryBytes, &rl.NanoCPUs)
	if errors.Is(err, sql.ErrNoRows) {
		return ResourceLimits{}, nil
	}
	if err != nil {
		return ResourceLimits{}, fmt.Errorf("scan resource_limits: %w", err)
	}
	return rl, nil
}

// SetResourceLimits upserts the limit policy for an instance. The FK to
// instances means an unknown instance_id fails the write (foreign_keys=ON).
func (s *Store) SetResourceLimits(instanceID string, rl ResourceLimits) error {
	_, err := s.db.Exec(
		`INSERT INTO instance_resource_limits (instance_id, memory_bytes, nano_cpus)
		 VALUES (?,?,?)
		 ON CONFLICT(instance_id) DO UPDATE SET
		   memory_bytes = excluded.memory_bytes,
		   nano_cpus    = excluded.nano_cpus`,
		instanceID, rl.MemoryBytes, rl.NanoCPUs)
	return err
}
