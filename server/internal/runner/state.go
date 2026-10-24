package runner

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/camp"
)

type record struct {
	InstallationID string                  `json:"installation_id"`
	Provider       string                  `json:"provider"`
	Epoch          int64                   `json:"epoch"`
	AttemptID      string                  `json:"attempt_id"`
	ClaimID        string                  `json:"claim_id"`
	DeliveryID     string                  `json:"delivery_id"`
	Phase          string                  `json:"phase"`
	CodeAttempted  bool                    `json:"code_attempted"`
	Report         *camp.RunnerLoginReport `json:"report,omitempty"`
}

type journal struct {
	root   *os.Root
	lock   *os.File
	cipher cipher.AEAD
	origin string
}

func privateFile(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrState
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !ownedPrivate(st) || !st.Mode().IsRegular() || st.Size() > limit {
		return nil, ErrState
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, ErrState
	}
	return data, nil
}

func ownedPrivate(st os.FileInfo) bool {
	if st.Mode().Perm()&0077 != 0 {
		return false
	}
	stat, ok := st.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}

func openJournal(dir, token, origin string) (*journal, error) {
	st, err := os.Lstat(dir)
	if err != nil || !ownedPrivate(st) || !st.IsDir() {
		return nil, ErrState
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, ErrState
	}
	lock, err := root.OpenFile("worker.lock", os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		root.Close()
		return nil, ErrState
	}
	info, err := lock.Stat()
	if err != nil || !ownedPrivate(info) || !info.Mode().IsRegular() || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		lock.Close()
		root.Close()
		return nil, errors.New("Runner state is already in use or is not private")
	}
	key := sha256.Sum256([]byte("capybara-runner-journal-v1:" + token))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		lock.Close()
		root.Close()
		return nil, ErrState
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		lock.Close()
		root.Close()
		return nil, ErrState
	}
	return &journal{root: root, lock: lock, cipher: aead, origin: origin}, nil
}

func (j *journal) close() {
	_ = j.lock.Close()
	_ = j.root.Close()
}

func (j *journal) load() (*record, error) {
	f, err := j.root.OpenFile("login.enc", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrState
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !ownedPrivate(st) || !st.Mode().IsRegular() || st.Size() > 384<<10 {
		return nil, ErrState
	}
	data, err := io.ReadAll(io.LimitReader(f, (384<<10)+1))
	n := j.cipher.NonceSize()
	if err != nil || len(data) <= n || len(data) > 384<<10 {
		return nil, ErrState
	}
	plain, err := j.cipher.Open(nil, data[:n], data[n:], []byte(j.origin))
	if err != nil {
		return nil, ErrState
	}
	var r record
	if json.Unmarshal(plain, &r) != nil || !validRecord(r) {
		return nil, ErrState
	}
	return &r, nil
}

func validRecord(r record) bool {
	for _, id := range []string{r.InstallationID, r.AttemptID, r.ClaimID, r.DeliveryID} {
		if _, err := uuid.Parse(id); err != nil {
			return false
		}
	}
	if r.Epoch < 1 || r.Provider != "Codex" && r.Provider != "Claude" {
		return false
	}
	switch r.Phase {
	case "prepared", "started":
		return r.Report == nil
	case "finishing", "done":
		return r.Report != nil && (r.Report.State == "connected" || r.Report.State == "failed")
	}
	return false
}

func (j *journal) save(r *record) error {
	if !validRecord(*r) {
		return ErrState
	}
	plain, err := json.Marshal(r)
	if err != nil || len(plain) > 320<<10 {
		return ErrState
	}
	nonce := make([]byte, j.cipher.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return ErrState
	}
	data := j.cipher.Seal(nonce, nonce, plain, []byte(j.origin))
	name := "journal-" + uuid.NewString()
	f, err := j.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return ErrState
	}
	defer j.root.Remove(name)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil || j.root.Rename(name, "login.enc") != nil {
		return ErrState
	}
	return j.syncDir()
}

func (j *journal) clear() error {
	if err := j.root.Remove("login.enc"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrState
	}
	return j.syncDir()
}

func (j *journal) syncDir() error {
	d, err := j.root.Open(".")
	if err != nil {
		return ErrState
	}
	defer d.Close()
	if d.Sync() != nil {
		return ErrState
	}
	return nil
}

// An installation container must have stopped the previous worker and all its
// children before restart. Remove only this adapter's abandoned scratch homes.
func (j *journal) cleanScratch() error {
	f, err := j.root.Open(".")
	if err != nil {
		return ErrState
	}
	entries, err := f.ReadDir(1025)
	f.Close()
	if err != nil && !errors.Is(err, io.EOF) || len(entries) > 1024 {
		return ErrState
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "provider-login-") || strings.HasPrefix(entry.Name(), "journal-") {
			if err := j.root.RemoveAll(filepath.Base(entry.Name())); err != nil {
				return ErrState
			}
		}
	}
	return j.syncDir()
}
