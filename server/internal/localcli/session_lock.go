package localcli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"github.com/google/uuid"
)

// Conversations and transfer verification use the same process-held locks.
// Empty files are ordinary process locks. A transfer marker also blocks a new
// conversation after process exit until an authenticated handoff is reconciled.
type sessionLocks struct {
	directory string
	files     map[string]*os.File
}

func (a *App) sessionLocks() (*sessionLocks, error) {
	if err := os.MkdirAll(a.Config.Support, 0700); err != nil {
		return nil, errors.New("Could not open Camp's local state")
	}
	return &sessionLocks{directory: a.Config.Support, files: map[string]*os.File{}}, nil
}

func (l *sessionLocks) close() {
	for _, file := range l.files {
		_ = file.Close()
	}
	l.files = nil
}

func (l *sessionLocks) acquire(key string) error {
	return l.acquireForHandoff(key, "")
}

func (l *sessionLocks) acquireForHandoff(key, handoff string) error {
	digest := sha256.Sum256([]byte(key))
	return l.acquireFile(key, hex.EncodeToString(digest[:]), handoff)
}

func (l *sessionLocks) acquireFile(key, digest, handoff string) error {
	if file := l.files[key]; file != nil {
		fence, err := readTransferFence(file)
		if err != nil || (fence != "" && fence != handoff) {
			return errors.New("This local session is held for a cloud transfer")
		}
		return nil
	}
	path := filepath.Join(l.directory, "run-"+digest+".lock")
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return errors.New("Could not acquire the local session lock")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByUser(info) || info.Mode().Perm()&0077 != 0 {
		file.Close()
		return errors.New("The local session lock is not private to this user")
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		file.Close()
		return errors.New("This session is already running or held for transfer in Capybara Camp")
	}
	fence, err := readTransferFence(file)
	if err != nil || (fence != "" && fence != handoff) {
		file.Close()
		return errors.New("This local session is held for a cloud transfer and cannot start locally until the transfer is reconciled")
	}
	l.files[key] = file
	return nil
}

const transferFencePrefix = "capybara-transfer-v1:"

func readTransferFence(file *os.File) (string, error) {
	data, err := io.ReadAll(io.LimitReader(io.NewSectionReader(file, 0, 128), 128))
	if err != nil || len(data) == 0 {
		return "", err
	}
	if len(data) != len(transferFencePrefix)+37 || string(data[:len(transferFencePrefix)]) != transferFencePrefix || data[len(data)-1] != '\n' {
		return "", errors.New("Invalid transfer fence")
	}
	id := string(data[len(transferFencePrefix) : len(data)-1])
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil || parsed.String() != id {
		return "", errors.New("Invalid transfer fence")
	}
	return id, nil
}

func (l *sessionLocks) fence(key, handoff string) error {
	id, err := uuid.Parse(handoff)
	if err != nil || id == uuid.Nil || id.String() != handoff {
		return errors.New("Invalid handoff identity")
	}
	file := l.files[key]
	if file == nil {
		return errors.New("Acquire the source before fencing it")
	}
	existing, err := readTransferFence(file)
	if err != nil || (existing != "" && existing != handoff) {
		return errors.New("Another transfer owns this source")
	}
	data := []byte(transferFencePrefix + handoff + "\n")
	if _, err = file.WriteAt(data, 0); err != nil {
		return err
	}
	if err = file.Truncate(int64(len(data))); err != nil {
		return err
	}
	if err = file.Sync(); err != nil {
		return err
	}
	directory, err := os.Open(l.directory)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (l *sessionLocks) releaseFence(key, handoff string) error {
	if id, err := uuid.Parse(handoff); err != nil || id == uuid.Nil || id.String() != handoff {
		return errors.New("Invalid handoff identity")
	}
	if err := l.acquireForHandoff(key, handoff); err != nil {
		return err
	}
	return l.releaseHeldFence(key, handoff)
}

func (l *sessionLocks) releaseHeldFence(key, handoff string) error {
	file := l.files[key]
	existing, err := readTransferFence(file)
	if err != nil || (existing != "" && existing != handoff) {
		return errors.New("Another transfer owns this source")
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	return file.Sync()
}
