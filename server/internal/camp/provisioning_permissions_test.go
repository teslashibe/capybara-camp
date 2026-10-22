package camp

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/teslashibe/capybara-camp/server/internal/database"
)

func TestControllerRoleCanProvisionWithoutReadingHumanOrProviderSecrets(t *testing.T) {
	s, actors := cloudFixture(t)
	ctx := context.Background()
	a := controllerAccount(t, s, actors[0])
	role := "camp_ctrl_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	password := uuid.NewString()
	if _, err := s.pool.Exec(ctx, `CREATE ROLE `+role+` LOGIN PASSWORD '`+password+`'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = s.pool.Exec(ctx, `DROP OWNED BY `+role); _, _ = s.pool.Exec(ctx, `DROP ROLE `+role) })
	var schema string
	if err := s.pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `GRANT USAGE ON SCHEMA `+schema+` TO `+role); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../database/migrations/00011_runner_provisioning.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := strings.Split(strings.Split(string(raw), "-- +goose StatementBegin")[1], "-- +goose StatementEnd")[0]
	if _, err = s.pool.Exec(ctx, strings.ReplaceAll(sql, "camp_controller", role)); err != nil {
		t.Fatal(err)
	}
	cfg := s.pool.Config().Copy()
	cfg.ConnConfig.User = role
	cfg.ConnConfig.Password = password
	limited, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer limited.Close()
	if err = database.Ready(ctx, limited); err != nil {
		t.Fatal("controller cannot check schema", err)
	}
	controller, err := NewWithProviderKey(limited, "unused", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	list, err := controller.ControllerAccounts(ctx, "", 64)
	if err != nil || len(list) != 1 {
		t.Fatal("controller cannot list installations", err)
	}
	if _, err = controller.EnsureControllerGrant(ctx, a, 4); err != nil {
		t.Fatal("controller cannot provision", err)
	}
	for _, query := range []string{`SELECT encrypted_credentials FROM provider_installations`, `SELECT encrypted_challenge FROM provider_login_attempts`, `SELECT encrypted_credentials FROM git_connections`, `SELECT encrypted_context FROM git_logins`, `SELECT email FROM users`, `SELECT token_hash FROM devices`} {
		if _, err = limited.Exec(ctx, query); err == nil {
			t.Fatal("controller read a prohibited column")
		}
	}
}
