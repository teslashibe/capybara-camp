package localcli

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// Conversations and transfer verification use the same process-held locks.
// File existence never means a session is locked; closing the descriptor,
// including after an abrupt process exit, releases ownership.
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
	if l.files[key] != nil {
		return nil
	}
	digest := sha256.Sum256([]byte(key))
	path := filepath.Join(l.directory, "run-"+hex.EncodeToString(digest[:])+".lock")
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
	l.files[key] = file
	return nil
}
