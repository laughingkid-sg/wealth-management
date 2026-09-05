// Package scriptstore owns the SQL for private.script_definitions — the global,
// append-only, versioned store of operator-authored Tengo scripts (pre-process
// and post-process). It has no browser grants; all access is via the service
// role, mirroring source_parser_rules. The scriptengine package runs the source
// this store returns; this package never executes scripts.
package scriptstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// maxSourceBytes mirrors the database CHECK and the engine's default source cap.
const maxSourceBytes = 64 * 1024

var (
	// ErrScriptNotFound is returned when a requested key/version does not exist.
	ErrScriptNotFound = errors.New("scriptstore: script not found")
	// ErrNoActiveScript is returned by LoadActiveScript when a key has no active
	// version. It is not an error condition for callers that treat "no script" as
	// "skip the stage"; check with errors.Is.
	ErrNoActiveScript = errors.New("scriptstore: no active script for key")

	scriptKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)
)

// Store reads and writes private.script_definitions.
type Store struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }

// ActiveScript is the worker-facing projection: the source to run plus the
// provenance a caller records as script:<key>:v<version>.
type ActiveScript struct {
	Key     string
	Version int
	Source  string
}

// ScriptVersion is one stored version of a script.
type ScriptVersion struct {
	Key       string
	Version   int
	Source    string
	Checksum  string
	IsActive  bool
	Notes     string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// ScriptSummary lists a key with its active version (0 = none) and version count.
type ScriptSummary struct {
	Key           string
	ActiveVersion int
	VersionCount  int
}

// Checksum is the canonical sha256 hex of a script source, matching the database
// CHECK. Exposed so callers and tests share one definition.
func Checksum(source string) string {
	sum := sha256.Sum256([]byte(source))
	return hex.EncodeToString(sum[:])
}

// ValidateKey reports whether a script key matches the stored contract.
func ValidateKey(key string) bool { return scriptKeyPattern.MatchString(key) }

// LoadActiveScript returns the active version for a key. It returns
// ErrNoActiveScript (wrapped) when the key has no active version so callers can
// treat that as "skip the stage".
func (s *Store) LoadActiveScript(ctx context.Context, key string) (ActiveScript, error) {
	if !ValidateKey(key) {
		return ActiveScript{}, fmt.Errorf("scriptstore: invalid script key %q", key)
	}
	var out ActiveScript
	err := s.pool.QueryRow(ctx, `
		select script_key, version, source
		from private.script_definitions
		where script_key = $1 and is_active = true`, key).Scan(&out.Key, &out.Version, &out.Source)
	if errors.Is(err, pgx.ErrNoRows) {
		return ActiveScript{}, fmt.Errorf("%w: %s", ErrNoActiveScript, key)
	}
	if err != nil {
		return ActiveScript{}, err
	}
	return out, nil
}

// ListScripts returns one summary per distinct key.
func (s *Store) ListScripts(ctx context.Context) ([]ScriptSummary, error) {
	rows, err := s.pool.Query(ctx, `
		select script_key,
			coalesce(max(version) filter (where is_active), 0) as active_version,
			count(*)::int as version_count
		from private.script_definitions
		group by script_key
		order by script_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	summaries := make([]ScriptSummary, 0)
	for rows.Next() {
		var summary ScriptSummary
		if err := rows.Scan(&summary.Key, &summary.ActiveVersion, &summary.VersionCount); err != nil {
			return nil, err
		}
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

// ListVersions returns every version for a key, newest first (without source).
func (s *Store) ListVersions(ctx context.Context, key string) ([]ScriptVersion, error) {
	if !ValidateKey(key) {
		return nil, fmt.Errorf("scriptstore: invalid script key %q", key)
	}
	rows, err := s.pool.Query(ctx, `
		select script_key, version, '' as source, checksum, is_active, coalesce(notes, ''), created_at, updated_at
		from private.script_definitions
		where script_key = $1
		order by version desc`, key)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	versions := make([]ScriptVersion, 0)
	for rows.Next() {
		var v ScriptVersion
		if err := rows.Scan(&v.Key, &v.Version, &v.Source, &v.Checksum, &v.IsActive, &v.Notes, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		versions = append(versions, v)
	}
	return versions, rows.Err()
}

// GetVersion returns one version including its source.
func (s *Store) GetVersion(ctx context.Context, key string, version int) (ScriptVersion, error) {
	if !ValidateKey(key) {
		return ScriptVersion{}, fmt.Errorf("scriptstore: invalid script key %q", key)
	}
	var v ScriptVersion
	err := s.pool.QueryRow(ctx, `
		select script_key, version, source, checksum, is_active, coalesce(notes, ''), created_at, updated_at
		from private.script_definitions
		where script_key = $1 and version = $2`, key, version).Scan(
		&v.Key, &v.Version, &v.Source, &v.Checksum, &v.IsActive, &v.Notes, &v.CreatedAt, &v.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ScriptVersion{}, ErrScriptNotFound
	}
	if err != nil {
		return ScriptVersion{}, err
	}
	return v, nil
}

// CreateVersion appends a new version for a key, computing the next version
// number and checksum. It does not activate the new version.
func (s *Store) CreateVersion(ctx context.Context, key, source, notes string, authorUserID uuid.UUID) (ScriptVersion, error) {
	if !ValidateKey(key) {
		return ScriptVersion{}, fmt.Errorf("scriptstore: invalid script key %q", key)
	}
	if len(source) == 0 || len(source) > maxSourceBytes {
		return ScriptVersion{}, fmt.Errorf("scriptstore: source must be 1..%d bytes", maxSourceBytes)
	}
	checksum := Checksum(source)
	var v ScriptVersion
	err := s.pool.QueryRow(ctx, `
		insert into private.script_definitions (script_key, version, source, checksum, is_active, notes, updated_by_user_id)
		values (
			$1,
			coalesce((select max(version) from private.script_definitions where script_key = $1), 0) + 1,
			$2, $3, false, nullif($4, ''), $5
		)
		returning script_key, version, source, checksum, is_active, coalesce(notes, ''), created_at, updated_at`,
		key, source, checksum, notes, authorUserID).Scan(
		&v.Key, &v.Version, &v.Source, &v.Checksum, &v.IsActive, &v.Notes, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return ScriptVersion{}, err
	}
	return v, nil
}

// Activate makes exactly one version of a key active (activate or rollback),
// deactivating any other active version in the same transaction.
func (s *Store) Activate(ctx context.Context, key string, version int) error {
	if !ValidateKey(key) {
		return fmt.Errorf("scriptstore: invalid script key %q", key)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx, `select exists(select 1 from private.script_definitions where script_key = $1 and version = $2)`, key, version).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrScriptNotFound
	}
	// Clear the current active version first so the one-active-per-key partial
	// unique index never sees two active rows.
	if _, err := tx.Exec(ctx, `update private.script_definitions set is_active = false where script_key = $1 and is_active = true and version <> $2`, key, version); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `update private.script_definitions set is_active = true where script_key = $1 and version = $2`, key, version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
