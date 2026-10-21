package camp

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/teslashibe/capybara-camp/server/internal/auth"
)

var ErrConflict = errors.New("request ID was reused with a different operation")

// The receipt commits in the same transaction as the domain write and event.
// Receipts can include invitation links, so their result is encrypted at rest.
func (s *Store) beginCommand(ctx context.Context, tx pgx.Tx, a auth.Actor, id, action string, input, output any) (bool, error) {
	if !validID(id) {
		return false, ErrInput
	}
	data, err := json.Marshal(input)
	if err != nil {
		return false, err
	}
	hash := sha256.Sum256(data)
	if _, err = tx.Exec(ctx, `INSERT INTO command_results(user_id,request_id,action,payload_hash) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, a.UserID, id, action, hash[:]); err != nil {
		return false, err
	}
	var oldAction string
	var oldHash, encrypted []byte
	err = tx.QueryRow(ctx, `SELECT action,payload_hash,encrypted_result FROM command_results WHERE user_id=$1 AND request_id=$2 FOR UPDATE`, a.UserID, id).Scan(&oldAction, &oldHash, &encrypted)
	if err != nil {
		return false, err
	}
	if oldAction != action || subtle.ConstantTimeCompare(hash[:], oldHash) != 1 {
		return false, ErrConflict
	}
	if encrypted == nil {
		return false, nil
	}
	block, err := s.receiptCipher()
	if err != nil {
		return false, err
	}
	n := block.NonceSize()
	if len(encrypted) < n {
		return false, errors.New("invalid receipt")
	}
	plain, err := block.Open(nil, encrypted[:n], encrypted[n:], []byte(a.UserID+":"+id))
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(plain, output)
}
func (s *Store) finishCommand(ctx context.Context, tx pgx.Tx, a auth.Actor, id string, output any) error {
	data, err := json.Marshal(output)
	if err != nil {
		return err
	}
	block, err := s.receiptCipher()
	if err != nil {
		return err
	}
	nonce := make([]byte, block.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	encrypted := block.Seal(nonce, nonce, data, []byte(a.UserID+":"+id))
	_, err = tx.Exec(ctx, `UPDATE command_results SET encrypted_result=$3 WHERE user_id=$1 AND request_id=$2`, a.UserID, id, encrypted)
	return err
}
func (s *Store) receiptCipher() (cipher.AEAD, error) {
	key := sha256.Sum256(append([]byte("capybara-command-receipts-v1:"), s.secret...))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}
