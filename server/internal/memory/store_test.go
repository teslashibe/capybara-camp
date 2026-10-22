package memory

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"testing"
)

func TestScopedDSNPreservesCredentialsAndQuotesSchema(t *testing.T) {
	path := `"memory","odd' schema",public`
	for _, dsn := range []string{"postgres://fixture:synthetic%20only@localhost/unit?sslmode=disable&search_path=old", `host=localhost user=fixture password='synthetic only' dbname=unit sslmode=disable search_path=old`} {
		parsed, err := pgxpool.ParseConfig(scopedDSN(dsn, path))
		if err != nil {
			t.Fatal("scoped configuration did not parse")
		}
		if parsed.ConnConfig.Password != "synthetic only" || parsed.ConnConfig.RuntimeParams["search_path"] != path || parsed.MaxConns != 4 {
			t.Fatal("scoped configuration changed connection settings")
		}
	}
	if SchemaName("public") != "camp_memory" || SchemaName("one") == SchemaName("two") {
		t.Fatal("schema isolation")
	}
}
