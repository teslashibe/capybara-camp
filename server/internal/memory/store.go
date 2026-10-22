// Package memory embeds Cortex in a private schema of Camp's database.
// It does not authorize users; the Camp domain supplies every scope and guard.
package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/teslashibe/cortex"
)

type Store struct {
	core   *cortex.Store
	base   *pgxpool.Pool
	schema string
}

func SchemaName(parent string) string {
	if parent == "public" {
		return "camp_memory"
	}
	digest := sha256.Sum256([]byte(parent))
	return "camp_memory_" + hex.EncodeToString(digest[:8])
}

func location(ctx context.Context, pool *pgxpool.Pool) (string, string, error) {
	var parent string
	if err := pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&parent); err != nil || parent == "" {
		return "", "", errors.New("memory schema unavailable")
	}
	schema := SchemaName(parent)
	path := pgx.Identifier{schema}.Sanitize() + "," + pgx.Identifier{parent}.Sanitize() + ",public"
	dsn := pool.Config().ConnString()
	return schema, scopedDSN(dsn, path), nil
}

func scopedDSN(dsn, path string) string {
	if u, err := url.Parse(dsn); err == nil && (u.Scheme == "postgres" || u.Scheme == "postgresql") {
		q := u.Query()
		q.Set("search_path", path)
		q.Set("pool_max_conns", "4")
		u.RawQuery = q.Encode()
		dsn = u.String()
	} else {
		escaped := strings.ReplaceAll(strings.ReplaceAll(path, `\`, `\\`), `'`, `\'`)
		dsn += " search_path='" + escaped + "' pool_max_conns=4"
	}
	return dsn
}

// Migrate is called by the explicit migration command, never by API startup.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	schema, dsn, err := location(ctx, pool)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(214016,1)`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize()); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	core, err := cortex.NewStore(ctx, dsn)
	if err != nil {
		return errors.New("memory database unavailable")
	}
	defer core.Close()
	if err = core.ApplyMigrations(ctx, cortex.Migrations()); err != nil {
		return errors.New("memory migration failed")
	}
	var runtime bool
	if err = pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='camp_runtime')`).Scan(&runtime); err != nil {
		return err
	}
	if runtime {
		if _, err = pool.Exec(ctx, "GRANT USAGE ON SCHEMA "+pgx.Identifier{schema}.Sanitize()+" TO camp_runtime"); err != nil {
			return err
		}
		rows, err := pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=$1 AND tablename<>'schema_version'`, schema)
		if err != nil {
			return err
		}
		var names []string
		for rows.Next() {
			var table string
			if err = rows.Scan(&table); err != nil {
				rows.Close()
				return err
			}
			names = append(names, pgx.Identifier{schema, table}.Sanitize())
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for _, table := range names {
			if _, err = pool.Exec(ctx, "GRANT SELECT,INSERT,UPDATE,DELETE ON TABLE "+table+" TO camp_runtime"); err != nil {
				return err
			}
		}
		if _, err = pool.Exec(ctx, "GRANT SELECT ON "+pgx.Identifier{schema, "schema_version"}.Sanitize()+" TO camp_runtime"); err != nil {
			return err
		}
	}
	return nil
}

func Open(ctx context.Context, pool *pgxpool.Pool) (*Store, error) {
	schema, dsn, err := location(ctx, pool)
	if err != nil {
		return nil, err
	}
	s := &Store{base: pool, schema: schema}
	if err = s.Ready(ctx); err != nil {
		return nil, err
	}
	s.core, err = cortex.NewStore(ctx, dsn)
	if err != nil {
		return nil, errors.New("memory database unavailable")
	}
	return s, nil
}
func (s *Store) Close() { s.core.Close() }
func (s *Store) Ready(ctx context.Context) error {
	entries, err := fs.ReadDir(cortex.Migrations(), ".")
	if err != nil {
		return err
	}
	want := 0
	count := 0
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".up.sql") {
			continue
		}
		if value, err := strconv.Atoi(strings.Split(entry.Name(), "_")[0]); err == nil {
			count++
			if value > want {
				want = value
			}
		}
	}
	var actual, installed int
	if err = s.base.QueryRow(ctx, "SELECT COALESCE(max(version),0),count(*) FROM "+pgx.Identifier{s.schema, "schema_version"}.Sanitize()).Scan(&actual, &installed); err != nil || actual != want || installed != count {
		return errors.New("memory schema is not compatible with this build")
	}
	return nil
}
func (s *Store) Observe(ctx context.Context, observations []cortex.Observation) error {
	n, err := cortex.NewEngine(s.core, nil).Observe(ctx, observations)
	if err != nil {
		return err
	}
	if n < 0 || n > len(observations) {
		return fmt.Errorf("invalid memory observation count")
	}
	// A zero count may mean an idempotent replay or a forgotten source. Do not
	// mark the latter as copied when Cortex intentionally refused resurrection.
	if n < len(observations) {
		for _, observation := range observations {
			var present bool
			if err = s.base.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM "+pgx.Identifier{s.schema, "observations"}.Sanitize()+" WHERE user_id=$1 AND id=$2 AND content=$3)", observation.UserID, observation.ID, observation.Content).Scan(&present); err != nil {
				return err
			}
			if !present {
				return errors.New("memory observation was not retained")
			}
		}
	}
	return nil
}
func (s *Store) Engine(model cortex.Model) *cortex.Engine { return cortex.NewEngine(s.core, model) }
func (s *Store) Outcome(ctx context.Context, space, id string) (cortex.Consolidation, error) {
	return s.core.Consolidation(ctx, space, id)
}
