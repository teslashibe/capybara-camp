// Package testdb creates an isolated schema in an explicitly supplied test DB.
package testdb

import (
	"context"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/teslashibe/capybara-camp/server/internal/database"
)

func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()
	value := os.Getenv("CAMP_TEST_DATABASE_URL")
	if value == "" {
		t.Skip("CAMP_TEST_DATABASE_URL is required for PostgreSQL integration tests")
	}
	ctx := context.Background()
	admin, err := database.Open(ctx, value)
	if err != nil {
		t.Fatal(err)
	}
	name := "camp_test_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := admin.Exec(ctx, "DROP SCHEMA "+name+" CASCADE")
		admin.Close()
		if err != nil {
			t.Error(err)
		}
	})
	u, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	if err = database.Migrate(ctx, u.String()); err != nil {
		t.Fatal(err)
	}
	pool, err := database.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = database.Ready(ctx, pool); err != nil {
		t.Fatal(err)
	}
	return pool
}
