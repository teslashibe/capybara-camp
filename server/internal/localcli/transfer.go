package localcli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

type transferHoldInput struct {
	Session   Session `json:"session"`
	Payload   []byte  `json:"payload"`
	ExpiresAt int64   `json:"expiresAt"`
	HandoffID string  `json:"handoffID,omitempty"`
}

// HoldTransferSource owns the native conversation lock until release, EOF,
// cancellation or expiry. It never starts or stops an external provider.
// The caller must retain this process while acknowledging the source hold.
func (a *App) HoldTransferSource(ctx context.Context, input io.ReadCloser, output io.Writer) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	stopClose := context.AfterFunc(ctx, func() { _ = input.Close() })
	defer stopClose()
	scanner := bufio.NewScanner(input)
	const inputLimit = (transfer.MaxWireBytes+2)/3*4 + (1 << 20)
	scanner.Buffer(make([]byte, 64<<10), inputLimit+1)
	if !scanner.Scan() || len(scanner.Bytes()) > inputLimit {
		return errors.New("Invalid source transfer request")
	}
	var in transferHoldInput
	decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil || decoder.Decode(new(any)) != io.EOF || len(in.Payload) > transfer.MaxWireBytes {
		return errors.New("Invalid source transfer request")
	}
	expires := time.Unix(in.ExpiresAt, 0)
	if !expires.After(time.Now()) || expires.After(time.Now().Add(5*time.Minute)) {
		return errors.New("The source transfer hold expired or has an invalid deadline")
	}
	ctx, deadlineCancel := context.WithDeadline(ctx, expires)
	defer deadlineCancel()
	session := in.Session
	if (session.Provider != "Codex" && session.Provider != "Claude") || !validSessionID(session.SessionID) || session.IsSubagent || !filepath.IsAbs(session.Root) || !filepath.IsAbs(session.CWD) {
		return errors.New("Choose a local parent session for this transfer")
	}
	var err error
	session.Root, err = canonical(session.Root, a.Config.Home)
	if err != nil || !isDir(session.Root) {
		return errors.New("Restore the source profile before transferring")
	}
	session.CWD, err = canonical(session.CWD, a.Config.Home)
	if err != nil || !isDir(session.CWD) {
		return errors.New("Restore the source project before transferring")
	}
	manifest, err := transfer.Decode(in.Payload)
	if err != nil {
		return err
	}
	review, err := manifest.Digest()
	if err != nil {
		return err
	}
	digest := sha256.Sum256(in.Payload)
	payloadHash := hex.EncodeToString(digest[:])
	in.Payload = nil
	locks, err := a.sessionLocks()
	if err != nil {
		return err
	}
	defer locks.close()
	key := session.Provider + ":" + session.Root + ":" + session.SessionID
	if err = locks.acquireForHandoff(key, in.HandoffID); err != nil {
		return err
	}
	verify := func() error {
		if err := a.assertAvailable(ctx, session); err != nil {
			return err
		}
		root, err := transfer.RepositoryRoot(ctx, session.CWD)
		if err != nil {
			return err
		}
		return transfer.VerifySource(ctx, root, manifest)
	}
	emit := func() error {
		if writer, ok := output.(interface{ SetWriteDeadline(time.Time) error }); ok {
			if err := writer.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
				return err
			}
		}
		return json.NewEncoder(output).Encode(map[string]string{"type": "verified", "payloadSHA256": payloadHash, "reviewSHA256": review})
	}
	if err = verify(); err != nil {
		return err
	}
	if err = emit(); err != nil {
		return err
	}
	commands := make(chan string, 1)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(commands)
		for scanner.Scan() {
			var command struct {
				Type string `json:"type"`
			}
			decoder := json.NewDecoder(bytes.NewReader(scanner.Bytes()))
			decoder.DisallowUnknownFields()
			if len(scanner.Bytes()) > 1024 || decoder.Decode(&command) != nil || decoder.Decode(new(any)) != io.EOF || (command.Type != "verify" && command.Type != "release" && command.Type != "prepareCommit") {
				return
			}
			select {
			case commands <- command.Type:
			case <-ctx.Done():
				return
			}
			if command.Type == "release" {
				return
			}
		}
	}()
	defer func() { cancel(); _ = input.Close(); <-readerDone }()
	timer := time.NewTicker(2 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case command, open := <-commands:
			if !open || command == "release" {
				return nil
			}
			if err = verify(); err != nil {
				return err
			}
			if command == "prepareCommit" {
				if err = locks.fence(key, in.HandoffID); err != nil {
					return err
				}
			}
			if err = emit(); err != nil {
				return err
			}
		case <-timer.C:
			if err = verify(); err != nil {
				return err
			}
		}
	}
}
