package camp

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/gitprovider"
)

// Workspace preparation does not transfer execution ownership or approve a
// provider turn. The runner must independently authorize those operations.
type WorkspaceInput struct {
	ProviderAccountID       string `json:"provider_account_id"`
	GitConnectionID         string `json:"git_connection_id"`
	GitHubInstallationID    int64  `json:"github_installation_id"`
	RepositoryID            int64  `json:"repository_id"`
	InstallationPage        int    `json:"installation_page"`
	RepositoryPage          int    `json:"repository_page"`
	Ref                     string `json:"ref"`
	Purpose                 string `json:"purpose"`
	ExpectedSessionRevision int64  `json:"expected_session_revision"`
	ExpectedProjectRevision int64  `json:"expected_project_revision"`
}

type Workspace struct {
	ID                     string    `json:"id"`
	CampID                 string    `json:"camp_id"`
	SessionID              string    `json:"session_id"`
	ProjectID              string    `json:"project_id"`
	OwnerUserID            string    `json:"owner_user_id"`
	ProviderAccountID      string    `json:"provider_account_id"`
	ProviderInstallationID string    `json:"provider_installation_id"`
	ProviderEpoch          int64     `json:"provider_epoch"`
	GitConnectionID        string    `json:"git_connection_id"`
	GitEpoch               int64     `json:"git_epoch"`
	GitHubUserID           int64     `json:"github_user_id"`
	GitHubLogin            string    `json:"github_login"`
	GitHubInstallationID   int64     `json:"github_installation_id"`
	RepositoryID           int64     `json:"repository_id"`
	RepositoryName         string    `json:"repository_name"`
	RequestedRef           string    `json:"requested_ref"`
	BaseCommit             string    `json:"base_commit"`
	Branch                 string    `json:"branch"`
	Purpose                string    `json:"purpose"`
	SourceRevision         int64     `json:"source_revision"`
	ProjectRevision        int64     `json:"project_revision"`
	State                  string    `json:"state"`
	Revision               int64     `json:"revision"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
	FailureCode            string    `json:"failure_code,omitempty"`
	TransferID             string    `json:"transfer_id,omitempty"`
}

type WorkspaceCancel struct {
	ExpectedRevision int64 `json:"expected_revision"`
}

type commitResolver interface {
	ResolveCommit(context.Context, string, gitprovider.Repository, string) (string, error)
}

type workspaceAuthority struct {
	session Session
	project Project
	account ProviderAccount
	git     GitConnection
}

func (g *GitStore) workspaceAuthority(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, sessionID string, input WorkspaceInput) (workspaceAuthority, error) {
	var out workspaceAuthority
	if _, err := g.store.lock(ctx, tx, a, campID, true); err != nil {
		return out, err
	}
	var err error
	out.session, err = g.store.readSession(ctx, tx, a, campID, sessionID)
	if err != nil {
		return out, err
	}
	if out.session.OwnerUserID != a.UserID {
		return out, ErrDenied
	}
	if out.session.HistoryOnly {
		return out, ErrTransition
	}
	out.project, err = readProject(ctx, tx, campID, out.session.ProjectID)
	if err != nil {
		return out, err
	}
	if out.project.Archived {
		return out, ErrTransition
	}
	out.account, err = g.store.lockAccount(ctx, tx, a.UserID, input.ProviderAccountID)
	if err != nil {
		return out, err
	}
	if out.account.Provider != out.session.Provider || out.account.State != "connected" || out.account.Location != "cloud" {
		return out, ErrTransition
	}
	if out.account.CredentialExpiresAt != nil && !out.account.CredentialExpiresAt.After(time.Now()) {
		return out, ErrTransition
	}
	out.git, err = g.lock(ctx, tx, a, input.GitConnectionID)
	if err != nil {
		return out, err
	}
	if out.git.State != "connected" {
		return out, gitprovider.ErrAuthorization
	}
	return out, nil
}

func validWorkspaceInput(input WorkspaceInput) bool {
	if !validID(input.ProviderAccountID) || !validID(input.GitConnectionID) || input.GitHubInstallationID <= 0 || input.RepositoryID <= 0 ||
		input.InstallationPage < 1 || input.InstallationPage > 10000 || input.RepositoryPage < 1 || input.RepositoryPage > 10000 ||
		input.ExpectedSessionRevision < 1 || input.ExpectedProjectRevision < 1 || !oneOf(input.Purpose, "planning", "implementation") ||
		len(input.Ref) > 512 || strings.ContainsFunc(input.Ref, unicode.IsControl) {
		return false
	}
	for _, part := range strings.Split(input.Ref, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

// CreateWorkspace freezes a GitHub-authorized base and queues preparation. Safe
// GitHub reads may repeat after a lost response; the row, branch and event do
// not. No credential, checkout, provider process or assignment is created here.
func (g *GitStore) CreateWorkspace(ctx context.Context, a auth.Actor, campID, sessionID, requestID string, input WorkspaceInput) (Workspace, error) {
	return g.createWorkspace(ctx, a, campID, sessionID, requestID, "", input)
}

func (g *GitStore) CreateTransferWorkspace(ctx context.Context, a auth.Actor, campID, sessionID, transferID, requestID string, input WorkspaceInput) (Workspace, error) {
	if !validID(transferID) {
		return Workspace{}, ErrInput
	}
	return g.createWorkspace(ctx, a, campID, sessionID, requestID, transferID, input)
}

func (g *GitStore) createWorkspace(ctx context.Context, a auth.Actor, campID, sessionID, requestID, transferID string, input WorkspaceInput) (Workspace, error) {
	if err := g.store.cloudEnabled(); err != nil {
		return Workspace{}, err
	}
	if !validWorkspaceInput(input) {
		return Workspace{}, ErrInput
	}
	resolver, ok := g.provider.(commitResolver)
	if !ok {
		return Workspace{}, ErrCloudUnavailable
	}
	work, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	tx, err := g.store.pool.Begin(work)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(work)
	if _, err = g.transferWorkspaceSource(work, tx, a, campID, sessionID, transferID, input.ProviderAccountID); err != nil {
		return Workspace{}, err
	}
	before, err := g.workspaceAuthority(work, tx, a, campID, sessionID, input)
	if err != nil {
		return Workspace{}, err
	}
	var existing Workspace
	action := "workspace.prepare:" + sessionID
	if transferID != "" {
		action = "transfer.workspace:" + transferID
	}
	if hit, err := g.store.beginCommand(work, tx, a, requestID, action, input, &existing); err != nil {
		return Workspace{}, err
	} else if hit {
		return readWorkspace(work, tx, a, campID, existing.ID)
	}
	if before.session.Revision != input.ExpectedSessionRevision || before.project.Revision != input.ExpectedProjectRevision {
		return Workspace{}, ErrRevision
	}
	// Save the request binding without holding locks across GitHub calls. An
	// incomplete binding is safe to resume because these calls only read GitHub.
	if err = tx.Commit(work); err != nil {
		return Workspace{}, err
	}
	token, err := g.token(work, a, input.GitConnectionID)
	if err != nil {
		return Workspace{}, err
	}
	installs, err := g.provider.Installations(work, token, input.InstallationPage)
	if err != nil {
		g.markRejected(work, a, input.GitConnectionID, token, err)
		return Workspace{}, err
	}
	var installation *gitprovider.Installation
	for i := range installs.Values {
		if installs.Values[i].ID == input.GitHubInstallationID {
			installation = &installs.Values[i]
			break
		}
	}
	if installation == nil || installation.Suspended || !oneOf(installation.Contents, "read", "write") {
		return Workspace{}, gitprovider.ErrAccess
	}
	repos, err := g.provider.Repositories(work, token, *installation, input.RepositoryPage)
	if err != nil {
		g.markRejected(work, a, input.GitConnectionID, token, err)
		return Workspace{}, err
	}
	var repository *gitprovider.Repository
	for i := range repos.Values {
		if repos.Values[i].ID == input.RepositoryID {
			repository = &repos.Values[i]
			break
		}
	}
	if repository == nil || repository.InstallationID != installation.ID || !repository.Read || (input.Purpose == "implementation" && !repository.Write) {
		return Workspace{}, gitprovider.ErrAccess
	}
	ref := input.Ref
	if ref == "" {
		ref = repository.DefaultBranch
	}
	sha, err := resolver.ResolveCommit(work, token, *repository, ref)
	if err != nil {
		g.markRejected(work, a, input.GitConnectionID, token, err)
		return Workspace{}, err
	}
	if len(sha) != 40 || strings.ContainsFunc(sha, func(c rune) bool { return !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') }) || ref == "" || len(ref) > 512 || strings.ContainsFunc(ref, unicode.IsControl) {
		return Workspace{}, gitprovider.ErrResponse
	}
	tx, err = g.store.pool.Begin(work)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(work)
	reviewed, err := g.transferWorkspaceSource(work, tx, a, campID, sessionID, transferID, input.ProviderAccountID)
	if err != nil {
		return Workspace{}, err
	}
	after, err := g.workspaceAuthority(work, tx, a, campID, sessionID, input)
	if err != nil {
		return Workspace{}, err
	}
	if hit, err := g.store.beginCommand(work, tx, a, requestID, action, input, &existing); err != nil {
		return Workspace{}, err
	} else if hit {
		return readWorkspace(work, tx, a, campID, existing.ID)
	}
	if after.git.Epoch != before.git.Epoch || after.account.Epoch != before.account.Epoch || after.session.Revision != input.ExpectedSessionRevision ||
		after.project.ID != before.project.ID || after.project.Revision != input.ExpectedProjectRevision {
		return Workspace{}, ErrRevision
	}
	if transferID != "" && sha != reviewed.BaseCommit {
		return Workspace{}, ErrRevision
	}
	linked := false
	for _, url := range after.project.Repositories {
		linked = linked || url == "https://github.com/"+strings.ToLower(repository.FullName)
	}
	if !linked {
		return Workspace{}, gitprovider.ErrAccess
	}
	var count int
	if err = tx.QueryRow(work, `SELECT count(*) FROM ticket_workspaces WHERE provider_installation_id=$1 AND state='queued'`, after.account.InstallationID).Scan(&count); err != nil {
		return Workspace{}, err
	}
	if count >= 25 {
		return Workspace{}, ErrTransition
	}
	id := uuid.NewString()
	_, err = tx.Exec(work, `INSERT INTO ticket_workspaces(id,camp_id,session_id,project_id,owner_user_id,provider_account_id,provider_installation_id,provider_epoch,
 git_connection_id,git_epoch,github_user_id,github_login,github_installation_id,repository_id,repository_name,requested_ref,base_commit,branch,purpose,source_revision,project_revision,installation_page,repository_page,transfer_id)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,NULLIF($24,'')::uuid)
 ON CONFLICT DO NOTHING`, id, campID, sessionID, after.project.ID, a.UserID, after.account.ID, after.account.InstallationID, after.account.Epoch,
		after.git.ID, after.git.Epoch, after.git.GitHubUserID, after.git.Login, installation.ID, repository.ID, repository.FullName, ref, sha, "camp/"+sessionID+"/"+id, input.Purpose, after.session.Revision, after.project.Revision, input.InstallationPage, input.RepositoryPage, transferID)
	if err != nil {
		return Workspace{}, err
	}
	out, err := readWorkspace(work, tx, a, campID, id)
	if errors.Is(err, ErrDenied) {
		return Workspace{}, ErrTransition
	}
	if err != nil {
		return Workspace{}, err
	}
	if err = g.emitWorkspace(work, tx, a, out); err != nil {
		return Workspace{}, err
	}
	if err = g.store.finishCommand(work, tx, a, requestID, out); err != nil {
		return Workspace{}, err
	}
	return out, tx.Commit(work)
}

const workspaceColumns = `w.id,w.camp_id,w.session_id,w.project_id,w.owner_user_id,w.provider_account_id,w.provider_installation_id,w.provider_epoch,
 w.git_connection_id,w.git_epoch,w.github_user_id,w.github_login,w.github_installation_id,w.repository_id,w.repository_name,w.requested_ref,w.base_commit,w.branch,w.purpose,
 w.source_revision,w.project_revision,w.state,w.revision,w.created_at,w.updated_at,w.failure_code,COALESCE(w.transfer_id::text,'')`

func readWorkspace(ctx context.Context, tx pgx.Tx, a auth.Actor, campID, id string) (Workspace, error) {
	var out Workspace
	if !validID(id) || !validID(campID) {
		return out, ErrDenied
	}
	err := tx.QueryRow(ctx, `SELECT `+workspaceColumns+` FROM ticket_workspaces w JOIN memberships m ON m.camp_id=w.camp_id AND m.user_id=$1
 WHERE w.owner_user_id=$1 AND w.camp_id=$2 AND w.id=$3`, a.UserID, campID, id).Scan(&out.ID, &out.CampID, &out.SessionID, &out.ProjectID, &out.OwnerUserID,
		&out.ProviderAccountID, &out.ProviderInstallationID, &out.ProviderEpoch, &out.GitConnectionID, &out.GitEpoch, &out.GitHubUserID, &out.GitHubLogin,
		&out.GitHubInstallationID, &out.RepositoryID, &out.RepositoryName, &out.RequestedRef, &out.BaseCommit, &out.Branch, &out.Purpose,
		&out.SourceRevision, &out.ProjectRevision, &out.State, &out.Revision, &out.CreatedAt, &out.UpdatedAt, &out.FailureCode, &out.TransferID)
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrDenied
	}
	return out, err
}

func (g *GitStore) Workspace(ctx context.Context, a auth.Actor, campID, id string) (Workspace, error) {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = g.store.lock(ctx, tx, a, campID, false); err != nil {
		return Workspace{}, err
	}
	return readWorkspace(ctx, tx, a, campID, id)
}

// SessionWorkspaces lets the owner resume preparation from another Mac without
// exposing account or repository identities to other Camp members.
func (g *GitStore) SessionWorkspaces(ctx context.Context, a auth.Actor, campID, sessionID string) ([]Workspace, error) {
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if _, err = g.store.lock(ctx, tx, a, campID, false); err != nil {
		return nil, err
	}
	session, err := g.store.readSession(ctx, tx, a, campID, sessionID)
	if err != nil {
		return nil, err
	}
	if session.OwnerUserID != a.UserID {
		return nil, ErrDenied
	}
	rows, err := tx.Query(ctx, `SELECT id FROM ticket_workspaces WHERE camp_id=$1 AND session_id=$2 AND owner_user_id=$3 ORDER BY updated_at DESC,id DESC LIMIT 25`, campID, sessionID, a.UserID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	out := []Workspace{}
	for _, id := range ids {
		w, err := readWorkspace(ctx, tx, a, campID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, nil
}

func (g *GitStore) CancelWorkspace(ctx context.Context, a auth.Actor, campID, id, requestID string, input WorkspaceCancel) (Workspace, error) {
	if input.ExpectedRevision < 1 {
		return Workspace{}, ErrInput
	}
	tx, err := g.store.pool.Begin(ctx)
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback(ctx)
	if _, err = g.store.lock(ctx, tx, a, campID, true); err != nil {
		return Workspace{}, err
	}
	out, err := readWorkspace(ctx, tx, a, campID, id)
	if err != nil {
		return out, err
	}
	var receipt Workspace
	if hit, err := g.store.beginCommand(ctx, tx, a, requestID, "workspace.cancel:"+id, input, &receipt); err != nil {
		return out, err
	} else if hit {
		return out, nil
	}
	if input.ExpectedRevision != out.Revision {
		return out, ErrRevision
	}
	if out.State != "cancelled" {
		if err = retireWorkspaceToken(ctx, tx, id); err != nil {
			return out, err
		}
		if _, err = tx.Exec(ctx, `UPDATE ticket_workspaces SET state='cancelled',revision=revision+1,updated_at=now() WHERE id=$1`, id); err != nil {
			return out, err
		}
		out, err = readWorkspace(ctx, tx, a, campID, id)
		if err != nil {
			return out, err
		}
		if err = g.emitWorkspace(ctx, tx, a, out); err != nil {
			return out, err
		}
	}
	if err = g.store.finishCommand(ctx, tx, a, requestID, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (g *GitStore) emitWorkspace(ctx context.Context, tx pgx.Tx, a auth.Actor, value Workspace) error {
	return g.store.emitWorkspace(ctx, tx, a, value)
}

func (s *Store) emitWorkspace(ctx context.Context, tx pgx.Tx, a auth.Actor, value Workspace) error {
	if err := emit(ctx, tx, a, value.CampID, "workspace.updated", map[string]any{"id": value.ID, "session_id": value.SessionID, "state": value.State, "revision": value.Revision}); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE events SET recipient_user_id=$2,session_id=$3 WHERE camp_id=$1 AND cursor=(SELECT event_cursor FROM camps WHERE id=$1)`, value.CampID, a.UserID, value.SessionID)
	return err
}
