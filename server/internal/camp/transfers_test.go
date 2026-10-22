package camp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func transferFixture(t *testing.T) (*Store, *pgxpool.Pool, []auth.Actor, Camp, Session) {
	t.Helper()
	_, pool, actors, c, project, profile := sessionFixture(t)
	s, err := NewWithProviderKey(pool, "fixture-receipt-key", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	session, err := s.PublishSession(context.Background(), actors[0], c.ID, uuid.NewString(), sessionInput(project, profile))
	if err != nil {
		t.Fatal(err)
	}
	return s, pool, actors, c, session
}

func transferPayload(t *testing.T, session Session, data []byte) (TransferInput, []byte) {
	t.Helper()
	m := transfer.Manifest{Version: 1, BaseCommit: strings.Repeat("a", 40), Branch: "main", Files: []transfer.File{{Path: "src/main.txt", SHA256: transferDigest(data), Data: data}}, Excluded: []transfer.Exclusion{}}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := m.Digest()
	if err != nil {
		t.Fatal(err)
	}
	return TransferInput{TargetLocation: "local", ExpectedEpoch: session.ExecutionEpoch, ExpectedRevision: session.Revision, ExpectedTranscriptRevision: session.TranscriptRevision, ExpectedPermissionRevision: session.PermissionRevision, PayloadSHA256: transferDigest(raw), ReviewSHA256: digest, PayloadBytes: len(raw)}, raw
}

func uploadTransfer(t *testing.T, s *Store, a auth.Actor, c Camp, session Session, value Transfer, raw []byte) Transfer {
	t.Helper()
	for index, offset := 0, 0; offset < len(raw); index, offset = index+1, offset+TransferChunkBytes {
		chunk := raw[offset:min(offset+TransferChunkBytes, len(raw))]
		var err error
		value, err = s.PutTransferChunk(context.Background(), a, c.ID, session.ID, value.ID, index, TransferChunk{Data: chunk, SHA256: transferDigest(chunk)})
		if err != nil {
			t.Fatal(err)
		}
	}
	return value
}

func TestTransferResumesEncryptedChunksWithoutMovingSession(t *testing.T) {
	s, pool, actors, c, session := transferFixture(t)
	ctx, owner := context.Background(), actors[0]
	in, raw := transferPayload(t, session, bytes.Repeat([]byte("reviewed source contents\n"), 24000))
	key := uuid.NewString()
	value, err := s.CreateTransfer(ctx, owner, c.ID, session.ID, key, in)
	if err != nil || value.State != "uploading" || value.TargetDeviceID != owner.DeviceID {
		t.Fatal(value, err)
	}
	replayed, err := s.CreateTransfer(ctx, owner, c.ID, session.ID, key, in)
	if err != nil || replayed.ID != value.ID {
		t.Fatal("create replay", err)
	}
	changed := in
	changed.ReviewSHA256 = strings.Repeat("b", 64)
	if _, err = s.CreateTransfer(ctx, owner, c.ID, session.ID, key, changed); !errors.Is(err, ErrConflict) {
		t.Fatal("changed replay", err)
	}
	if _, err = s.TransferChunk(ctx, owner, c.ID, session.ID, value.ID, 0); !errors.Is(err, ErrTransition) {
		t.Fatal("unverified payload readable", err)
	}
	if _, err = s.CompleteTransfer(ctx, owner, c.ID, session.ID, value.ID); !errors.Is(err, ErrTransition) {
		t.Fatal("incomplete payload completed", err)
	}
	// Upload the final chunk first, then resume after reconstructing the store.
	last := (len(raw) - 1) / TransferChunkBytes
	part := raw[last*TransferChunkBytes:]
	value, err = s.PutTransferChunk(ctx, owner, c.ID, session.ID, value.ID, last, TransferChunk{Data: part, SHA256: transferDigest(part)})
	if err != nil || !reflect.DeepEqual(value.Uploaded, []int{last}) {
		t.Fatal("out of order upload", err)
	}
	s, err = NewWithProviderKey(pool, "fixture-receipt-key", bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	listed, err := s.ListTransfers(ctx, owner, c.ID, session.ID)
	if err != nil || len(listed) != 1 || listed[0].UploadedBytes != len(part) {
		t.Fatal("restart lost progress", err)
	}
	value = uploadTransfer(t, s, owner, c, session, value, raw)
	if value.UploadedBytes != len(raw) {
		t.Fatal("duplicate chunk counted twice")
	}
	var encrypted []byte
	if err = pool.QueryRow(ctx, `SELECT encrypted_data FROM transfer_chunks WHERE transfer_id=$1 AND ordinal=0`, value.ID).Scan(&encrypted); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, raw[:64]) || bytes.Equal(encrypted, raw[:TransferChunkBytes]) {
		t.Fatal("stored plaintext")
	}
	value, err = s.CompleteTransfer(ctx, owner, c.ID, session.ID, value.ID)
	if err != nil || value.State != "ready" {
		t.Fatal("complete", err)
	}
	if value.BaseCommit != strings.Repeat("a", 40) || value.FileCount != 1 {
		t.Fatal("missing reviewed base metadata")
	}
	// A schema-20 upload keeps its ciphertext and is validated once to populate
	// the additive metadata needed by destination preparation.
	if _, err = pool.Exec(ctx, `UPDATE session_transfers SET base_commit=NULL,source_branch=NULL,file_count=NULL WHERE id=$1`, value.ID); err != nil {
		t.Fatal(err)
	}
	value, err = s.CompleteTransfer(ctx, owner, c.ID, session.ID, value.ID)
	if err != nil || value.BaseCommit != strings.Repeat("a", 40) || value.FileCount != 1 {
		t.Fatal("upgraded upload did not resume", err)
	}
	revision := value.Revision
	value, err = s.CompleteTransfer(ctx, owner, c.ID, session.ID, value.ID)
	if err != nil || value.Revision != revision {
		t.Fatal("completion replay", err)
	}
	first := raw[:TransferChunkBytes]
	value, err = s.PutTransferChunk(ctx, owner, c.ID, session.ID, value.ID, 0, TransferChunk{Data: first, SHA256: transferDigest(first)})
	if err != nil || value.Revision != revision {
		t.Fatal("late chunk replay", err)
	}
	other := bytes.Repeat([]byte("!"), len(first))
	if _, err = s.PutTransferChunk(ctx, owner, c.ID, session.ID, value.ID, 0, TransferChunk{Data: other, SHA256: transferDigest(other)}); !errors.Is(err, ErrConflict) {
		t.Fatal("changed chunk retry accepted", err)
	}
	var assembled []byte
	for index := range value.Uploaded {
		chunk, err := s.TransferChunk(ctx, owner, c.ID, session.ID, value.ID, index)
		if err != nil || transferDigest(chunk.Data) != chunk.SHA256 {
			t.Fatal("read payload", err)
		}
		assembled = append(assembled, chunk.Data...)
	}
	if !bytes.Equal(assembled, raw) {
		t.Fatal("payload changed")
	}
	current, err := s.Session(ctx, owner, c.ID, session.ID)
	if err != nil || !reflect.DeepEqual(current, session) {
		t.Fatal("upload changed source session", err)
	}
	for _, other := range actors[1:] {
		if _, err = s.CreateTransfer(ctx, other, c.ID, session.ID, uuid.NewString(), in); !errors.Is(err, ErrDenied) {
			t.Fatal("foreign create", err)
		}
		if _, err = s.PutTransferChunk(ctx, other, c.ID, session.ID, value.ID, 0, TransferChunk{Data: first, SHA256: transferDigest(first)}); !errors.Is(err, ErrDenied) {
			t.Fatal("foreign upload", err)
		}
		if _, err = s.CompleteTransfer(ctx, other, c.ID, session.ID, value.ID); !errors.Is(err, ErrDenied) {
			t.Fatal("foreign complete", err)
		}
		if _, err = s.Transfer(ctx, other, c.ID, session.ID, value.ID); !errors.Is(err, ErrDenied) {
			t.Fatal("foreign transfer metadata", err)
		}
		if _, err = s.ListTransfers(ctx, other, c.ID, session.ID); !errors.Is(err, ErrDenied) {
			t.Fatal("foreign transfer list", err)
		}
		if _, err = s.TransferChunk(ctx, other, c.ID, session.ID, value.ID, 0); !errors.Is(err, ErrDenied) {
			t.Fatal("foreign payload", err)
		}
		if _, err = s.CancelTransfer(ctx, other, c.ID, session.ID, value.ID); !errors.Is(err, ErrDenied) {
			t.Fatal("foreign cancel", err)
		}
	}
	events, err := s.Events(ctx, actors[1], c.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Kind == "transfer.updated" {
			t.Fatal("private transfer event shared")
		}
	}
}

func TestTransferRejectsChangedSourceAndTarget(t *testing.T) {
	s, pool, actors, c, session := transferFixture(t)
	ctx, owner := context.Background(), actors[0]
	in, raw := transferPayload(t, session, []byte("reviewed source"))
	for _, field := range []string{"execution_epoch", "revision", "transcript_revision", "permission_revision"} {
		t.Run(field, func(t *testing.T) {
			value, err := s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), in)
			if err != nil {
				t.Fatal(err)
			}
			value = uploadTransfer(t, s, owner, c, session, value, raw)
			if _, err = pool.Exec(ctx, "UPDATE sessions SET "+field+"="+field+"+1 WHERE id=$1", session.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.CompleteTransfer(ctx, owner, c.ID, session.ID, value.ID); !errors.Is(err, ErrRevision) {
				t.Fatal("stale source accepted", err)
			}
			if _, err = pool.Exec(ctx, "UPDATE sessions SET "+field+"="+field+"-1 WHERE id=$1", session.ID); err != nil {
				t.Fatal(err)
			}
			if _, err = s.CancelTransfer(ctx, owner, c.ID, session.ID, value.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, state := range []string{"running", "unknown"} {
		if _, err := pool.Exec(ctx, `UPDATE sessions SET state=$2 WHERE id=$1`, session.ID, state); err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), in); !errors.Is(err, ErrTransition) {
			t.Fatal("busy source accepted", err)
		}
	}
	if _, err := pool.Exec(ctx, `UPDATE sessions SET state='idle' WHERE id=$1`, session.ID); err != nil {
		t.Fatal(err)
	}
	request, err := s.CreateRequest(ctx, owner, c.ID, session.ID, uuid.NewString(), RequestInput{Prompt: "Pending work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), in); !errors.Is(err, ErrTransition) {
		t.Fatal("queued prompt did not block transfer", err)
	}
	if _, err = s.ActOnRequest(ctx, owner, c.ID, request.ID, uuid.NewString(), RequestAction{Action: "cancel"}); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE sessions SET external_owner=true WHERE id=$1`, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), in); !errors.Is(err, ErrTransition) {
		t.Fatal("external owner did not block transfer", err)
	}
	if _, err = pool.Exec(ctx, `UPDATE sessions SET external_owner=false WHERE id=$1`, session.ID); err != nil {
		t.Fatal(err)
	}
	account, grant := addCloud(t, s, owner, "Codex")
	login, claim := startCloudLogin(t, s, owner, account, grant)
	if _, err := s.ReportRunnerLogin(ctx, grant.Token, login.ID, claim, loginProof("owner-subject")); err != nil {
		t.Fatal(err)
	}
	in.TargetLocation, in.TargetAccountID = "cloud", account.ID
	value, err := s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	value = uploadTransfer(t, s, owner, c, session, value, raw)
	if _, err = pool.Exec(ctx, `UPDATE provider_installations SET epoch=epoch+1 WHERE account_id=$1`, account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteTransfer(ctx, owner, c.ID, session.ID, value.ID); !errors.Is(err, ErrRevision) {
		t.Fatal("changed destination identity accepted", err)
	}
	foreign, _ := addCloud(t, s, actors[1], "Codex")
	in.TargetAccountID = foreign.ID
	if _, err = s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), in); !errors.Is(err, ErrDenied) {
		t.Fatal("foreign destination accepted", err)
	}
	in.TargetAccountID = account.ID
	if _, err = pool.Exec(ctx, `UPDATE provider_installations SET credential_expires_at=now()-interval '1 second' WHERE account_id=$1`, account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), in); !errors.Is(err, ErrTransition) {
		t.Fatal("expired destination accepted", err)
	}
	otherProvider, otherGrant := addCloud(t, s, owner, "Claude")
	otherLogin, otherClaim := startCloudLogin(t, s, owner, otherProvider, otherGrant)
	if _, err = s.ReportRunnerLogin(ctx, otherGrant.Token, otherLogin.ID, otherClaim, loginProof("other-provider")); err != nil {
		t.Fatal(err)
	}
	in.TargetAccountID = otherProvider.ID
	if _, err = s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), in); !errors.Is(err, ErrTransition) {
		t.Fatal("different provider destination accepted", err)
	}
}

func TestTransferQuotaSerializesAcrossCamps(t *testing.T) {
	s, _, actors, c, session := transferFixture(t)
	ctx, owner := context.Background(), actors[0]
	otherCamp, err := s.Create(ctx, owner, uuid.NewString(), "Other Camp")
	if err != nil {
		t.Fatal(err)
	}
	project, err := s.CreateProject(ctx, owner, otherCamp.ID, uuid.NewString(), "Project")
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := s.PublishSession(ctx, owner, otherCamp.ID, uuid.NewString(), SessionInput{ID: uuid.NewString(), ProjectID: project.ID, ProfileID: session.ProfileID, LocalSessionID: uuid.NewString(), Title: "Other source", State: "idle", Stage: "Ready"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			targetCamp, targetSession := c, session
			if i%2 == 0 {
				targetCamp, targetSession = otherCamp, otherSession
			}
			input, _ := transferPayload(t, targetSession, []byte("source"))
			_, err := s.CreateTransfer(ctx, owner, targetCamp.ID, targetSession.ID, uuid.NewString(), input)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	accepted := 0
	for err := range errs {
		if err == nil {
			accepted++
		} else if !errors.Is(err, ErrTransition) {
			t.Fatal(err)
		}
	}
	if accepted != 2 {
		t.Fatalf("quota across Camps admitted %d transfers", accepted)
	}
}

func TestTransferRejectsInvalidAndSwappedPayloads(t *testing.T) {
	s, pool, actors, c, session := transferFixture(t)
	ctx, owner := context.Background(), actors[0]
	in, raw := transferPayload(t, session, []byte("content to verify"))
	for _, kind := range []string{"wire hash", "review hash", "invalid manifest", "secret content"} {
		t.Run(kind, func(t *testing.T) {
			input, payload := in, raw
			switch kind {
			case "wire hash":
				input.PayloadSHA256 = strings.Repeat("b", 64)
			case "review hash":
				input.ReviewSHA256 = strings.Repeat("b", 64)
			case "invalid manifest":
				payload = []byte(`{"version":100}`)
			case "secret content":
				secret := []byte("-----BEGIN PRIVATE KEY-----")
				m := transfer.Manifest{Version: 1, BaseCommit: strings.Repeat("a", 40), Files: []transfer.File{{Path: "src/config", Data: secret, SHA256: transferDigest(secret)}}}
				payload, _ = json.Marshal(m)
			}
			if kind == "invalid manifest" || kind == "secret content" {
				input.PayloadBytes, input.PayloadSHA256 = len(payload), transferDigest(payload)
			}
			value, err := s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), input)
			if err != nil {
				t.Fatal(err)
			}
			value = uploadTransfer(t, s, owner, c, session, value, payload)
			if _, err = s.CompleteTransfer(ctx, owner, c.ID, session.ID, value.ID); !errors.Is(err, ErrInput) {
				t.Fatal("invalid payload accepted", err)
			}
			if _, err = s.CancelTransfer(ctx, owner, c.ID, session.ID, value.ID); err != nil {
				t.Fatal(err)
			}
		})
	}
	first, err := s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), in)
	if err != nil {
		t.Fatal(err)
	}
	first = uploadTransfer(t, s, owner, c, session, first, raw)
	second = uploadTransfer(t, s, owner, c, session, second, raw)
	if _, err = pool.Exec(ctx, `UPDATE transfer_chunks SET encrypted_data=(SELECT encrypted_data FROM transfer_chunks WHERE transfer_id=$2 AND ordinal=0) WHERE transfer_id=$1 AND ordinal=0`, second.ID, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.CompleteTransfer(ctx, owner, c.ID, session.ID, second.ID); !errors.Is(err, ErrInput) {
		t.Fatal("ciphertext swapped across transfers", err)
	}
	for _, index := range []int{-1, int(^uint(0) >> 1)} {
		if _, err = s.PutTransferChunk(ctx, owner, c.ID, session.ID, first.ID, index, TransferChunk{Data: raw, SHA256: transferDigest(raw)}); !errors.Is(err, ErrInput) {
			t.Fatal("invalid index accepted", err)
		}
	}
}

func TestTransferQuotaExpiryCancelAndCompletionCapacity(t *testing.T) {
	s, pool, actors, c, session := transferFixture(t)
	ctx, owner := context.Background(), actors[0]
	in, raw := transferPayload(t, session, []byte("source"))
	var wg sync.WaitGroup
	results := make(chan Transfer, 6)
	errs := make(chan error, 6)
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), in)
			if err == nil {
				results <- v
			} else {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	if len(results) != 2 || len(errs) != 4 {
		t.Fatalf("concurrent quota: %d successes, %d errors", len(results), len(errs))
	}
	for err := range errs {
		if !errors.Is(err, ErrTransition) {
			t.Fatal(err)
		}
	}
	first, second := <-results, <-results
	first = uploadTransfer(t, s, owner, c, session, first, raw)
	if _, err := pool.Exec(ctx, `UPDATE session_transfers SET expires_at=now()-interval '1 second' WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	expired, err := s.Transfer(ctx, owner, c.ID, session.ID, first.ID)
	if err != nil || expired.State != "expired" || expired.UploadedBytes != 0 || len(expired.Uploaded) != 0 {
		t.Fatal("expiry retained payload", expired, err)
	}
	if _, err = s.CreateTransfer(ctx, owner, c.ID, session.ID, uuid.NewString(), in); err != nil {
		t.Fatal("expiry did not free quota", err)
	}
	second = uploadTransfer(t, s, owner, c, session, second, raw)
	cancelled, err := s.CancelTransfer(ctx, owner, c.ID, session.ID, second.ID)
	if err != nil || cancelled.State != "cancelled" || cancelled.UploadedBytes != 0 || len(cancelled.Uploaded) != 0 {
		t.Fatal("cancel retained payload", err)
	}
	replay, err := s.CancelTransfer(ctx, owner, c.ID, session.ID, second.ID)
	if err != nil || replay.Revision != cancelled.Revision {
		t.Fatal("cancel replay", err)
	}
	for range cap(s.transferSlots) {
		s.transferSlots <- struct{}{}
	}
	if _, err = s.CompleteTransfer(ctx, owner, c.ID, session.ID, second.ID); !errors.Is(err, auth.ErrRateLimited) {
		t.Fatal("completion admission did not reject excess work", err)
	}
	for len(s.transferSlots) > 0 {
		<-s.transferSlots
	}
}
