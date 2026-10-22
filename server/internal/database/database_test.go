package database

import (
	"context"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestMigrationPreservesExistingIdentityData(t *testing.T) {
	raw := os.Getenv("CAMP_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("CAMP_TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, err := Open(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	schema := "camp_upgrade_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	pool, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()
	files, err := fs.Sub(migrations, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.UpTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO users(id,email,display_name) VALUES($1,'upgrade@example.com','Preserved user')`, id); err != nil {
		t.Fatal(err)
	}
	if Ready(ctx, pool) == nil {
		t.Fatal("old schema declared compatible")
	}
	if _, err = provider.UpTo(ctx, 6); err != nil {
		t.Fatal(err)
	}
	camp, project, plan := uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO camps(id,name) VALUES($1,'Migration fixture')`, camp); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO projects(id,camp_id,name) VALUES($1,$2,'Preserved project')`, project, camp); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO planning_tickets(id,camp_id,project_id,creator_user_id,title) VALUES($1,$2,$3,$4,'Saved plan')`, plan, camp, project, id); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO planning_versions(plan_id,version,state,brief,sources,budget_policy) VALUES($1,1,'ready','Retain the old comparison','[]','calls')`, plan); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	if err = Migrate(ctx, u.String()); err != nil {
		t.Fatal("repeated migration failed", err)
	}
	if err = Ready(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var name string
	if err = pool.QueryRow(ctx, `SELECT display_name FROM users WHERE id=$1`, id).Scan(&name); err != nil || name != "Preserved user" {
		t.Fatal("identity data lost", err)
	}
	var mode, brief string
	var limit int
	if err = pool.QueryRow(ctx, `SELECT mode,brief,call_limit FROM planning_versions WHERE plan_id=$1 AND version=1`, plan).Scan(&mode, &brief, &limit); err != nil || mode != "comparison" || limit != 5 || brief != "Retain the old comparison" {
		t.Fatal("planning migration lost existing history", err)
	}
}
