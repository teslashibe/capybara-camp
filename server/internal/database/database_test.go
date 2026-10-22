package database

import (
	"bytes"
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
	if _, err = provider.UpTo(ctx, 12); err != nil {
		t.Fatal(err)
	}
	account, installation, connection := uuid.NewString(), uuid.NewString(), uuid.NewString()
	credentialFixture := []byte("encrypted-fixture-preserved-by-upgrade")
	if _, err = pool.Exec(ctx, `INSERT INTO provider_accounts(id,owner_user_id,provider,label) VALUES($1,$2,'Codex','Preserved account')`, account, id); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO provider_installations(id,account_id,owner_user_id,location,state,epoch,credential_revision,encrypted_credentials) VALUES($1,$2,$3,'cloud','connected',3,4,$4)`, installation, account, id, credentialFixture); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO git_connections(id,owner_user_id,github_user_id,github_login,state,epoch,encrypted_credentials) VALUES($1,$2,123,'preserved-user','connected',5,$3)`, connection, id, credentialFixture); err != nil {
		t.Fatal(err)
	}
	device, profile, session, request := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO devices(id,user_id,name,token_hash,expires_at) VALUES($1,$2,'Preserved Mac',$3,now()+interval '1 day')`, device, id, bytes.Repeat([]byte{17}, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO profiles(id,owner_user_id,owner_device_id,provider,local_id,label) VALUES($1,$2,$3,'Codex',$4,'Preserved profile')`, profile, id, device, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO sessions(id,camp_id,project_id,profile_id,owner_user_id,owner_device_id,local_session_id,title,state,stage,shared) VALUES($1,$2,$3,$4,$5,$6,'preserved-session','Preserved session','idle','Ready',false)`, session, camp, project, profile, id, device); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO prompt_requests(id,camp_id,session_id,requester_user_id,owner_user_id,owner_device_id,permission_revision,prompt,model,effort,speed,state) VALUES($1,$2,$3,$4,$4,$5,1,'Preserved queued prompt','fixture-model','high','standard','queued')`, request, camp, session, id, device); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.UpTo(ctx, 16); err != nil {
		t.Fatal(err)
	}
	workspace := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO ticket_workspaces(id,camp_id,session_id,project_id,owner_user_id,provider_account_id,provider_installation_id,provider_epoch,git_connection_id,git_epoch,github_user_id,github_login,github_installation_id,repository_id,repository_name,requested_ref,base_commit,branch,purpose,source_revision,project_revision,state)
 VALUES($1,$2,$3,$4,$5,$6,$7,3,$8,5,123,'preserved-user',7,5,'fixture/repo','main',$9,$10,'planning',1,1,'ready')`, workspace, camp, session, project, id, account, installation, connection, strings.Repeat("a", 40), "camp/"+session+"/"+workspace); err != nil {
		t.Fatal(err)
	}
	controlID := uuid.NewString()
	if _, err = pool.Exec(ctx, `INSERT INTO request_controls(id,request_id,provider_id,kind,input_hash,accepted_text,state) VALUES($1,$2,$4,'steer',$3,'Preserved direction','resolved')`, controlID, request, bytes.Repeat([]byte{19}, 32), controlID); err != nil {
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
	var resolution string
	if err = pool.QueryRow(ctx, `SELECT resolution FROM request_controls WHERE id=$1`, controlID).Scan(&resolution); err != nil || resolution != "accepted" {
		t.Fatal("control resolution migration lost accepted direction", resolution, err)
	}
	var preparedState, preparedCommit string
	if err = pool.QueryRow(ctx, `SELECT state,base_commit FROM ticket_workspaces WHERE id=$1`, workspace).Scan(&preparedState, &preparedCommit); err != nil || preparedState != "ready" || preparedCommit != strings.Repeat("a", 40) {
		t.Fatal("scheduling migration lost prepared checkout", err)
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
	var credentials []byte
	var epoch, revision int64
	if err = pool.QueryRow(ctx, `SELECT encrypted_credentials,epoch,credential_revision FROM provider_installations WHERE id=$1`, installation).Scan(&credentials, &epoch, &revision); err != nil || !bytes.Equal(credentials, credentialFixture) || epoch != 3 || revision != 4 {
		t.Fatal("workspace migration changed provider credentials", err)
	}
	if err = pool.QueryRow(ctx, `SELECT encrypted_credentials,epoch FROM git_connections WHERE id=$1`, connection).Scan(&credentials, &epoch); err != nil || !bytes.Equal(credentials, credentialFixture) || epoch != 5 {
		t.Fatal("workspace migration changed Git credentials", err)
	}
	var location, requestMode, prompt string
	if err = pool.QueryRow(ctx, `SELECT execution_location,execution_epoch FROM sessions WHERE id=$1`, session).Scan(&location, &epoch); err != nil || location != "local" || epoch != 1 {
		t.Fatal("session migrated away from Local", err)
	}
	if err = pool.QueryRow(ctx, `SELECT execution_location,execution_epoch,execution_mode,prompt FROM prompt_requests WHERE id=$1`, request).Scan(&location, &epoch, &requestMode, &prompt); err != nil || location != "local" || epoch != 1 || requestMode != "chat" || prompt != "Preserved queued prompt" {
		t.Fatal("queued request changed during migration", err)
	}
}
