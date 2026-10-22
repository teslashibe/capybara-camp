package localcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/gitcheckout"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

type TransferCheckoutInput struct {
	Scope      string                 `json:"scope"`
	TransferID string                 `json:"transferID"`
	Repository string                 `json:"repository"`
	BaseCommit string                 `json:"baseCommit"`
	Branch     string                 `json:"branch"`
	Credential gitcheckout.Credential `json:"credential"`
}
type TransferCheckoutReceipt struct {
	Version     int    `json:"version"`
	Scope       string `json:"scope"`
	TransferID  string `json:"transferID"`
	Repository  string `json:"repository"`
	BaseCommit  string `json:"baseCommit"`
	Branch      string `json:"branch"`
	Destination string `json:"destination"`
}

// Prepare in an unpublished scratch directory, then atomically publish a clean
// checkout. Replays never reset or remove a published checkout. Credentials are
// confined to a temporary private file used by the exact-repository Git helper.
func (a *App) prepareTransferCheckout(ctx context.Context, input io.Reader) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	raw, err := io.ReadAll(io.LimitReader(input, 16385))
	if err != nil || len(raw) > 16384 {
		return nil, transfer.ErrLimit
	}
	var in TransferCheckoutInput
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if dec.Decode(&in) != nil || dec.Decode(new(any)) != io.EOF || !installDigest(in.Scope) || !installUUID(in.TransferID) || !gitcheckout.ValidRepository(in.Repository) || !gitcheckout.ValidCommit(in.BaseCommit) || in.Branch == "" || len(in.Branch) > 512 || strings.ContainsFunc(in.Branch, unicode.IsControl) {
		return nil, transfer.ErrInput
	}
	remote := "https://github.com/" + in.Repository + ".git"
	if in.Credential.URL != remote || !gitcheckout.ValidToken(in.Credential.Token) || !in.Credential.ExpiresAt.After(time.Now()) || in.Credential.ExpiresAt.After(time.Now().Add(time.Hour)) {
		return nil, transfer.ErrInput
	}
	state, err := a.transferStateDirectory(ctx, in.Scope, in.TransferID)
	if err != nil {
		return nil, err
	}
	stateInfo, err := os.Lstat(state)
	if err != nil || !stateInfo.IsDir() || !ownedByUser(stateInfo) || stateInfo.Mode().Perm()&0077 != 0 {
		return nil, transfer.ErrDestination
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	lock, err := root.OpenFile("stage.lock", os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0600)
	if err != nil {
		return nil, transfer.ErrDestination
	}
	defer lock.Close()
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByUser(info) || info.Mode().Perm()&0077 != 0 || info.Sys().(*syscall.Stat_t).Nlink != 1 || syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return nil, transfer.ErrStageBusy
	}
	if err = cleanCheckoutCredentials(root); err != nil {
		return nil, err
	}
	support, err := os.OpenRoot(a.Config.Support)
	if err != nil {
		return nil, err
	}
	defer support.Close()
	canonical, err := filepath.EvalSymlinks(a.Config.Support)
	if err != nil {
		return nil, err
	}
	name := "workspace-" + in.Scope + "-" + in.TransferID
	destination := filepath.Join(canonical, name)
	receipt := TransferCheckoutReceipt{Version: 1, Scope: in.Scope, TransferID: in.TransferID, Repository: in.Repository, BaseCommit: in.BaseCommit, Branch: in.Branch, Destination: destination}
	encoded, _ := json.Marshal(receipt)
	prior, err := readInstallFile(root, "checkout-intent.json", 16384)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	_, destinationErr := support.Lstat(name)
	if destinationErr != nil && !os.IsNotExist(destinationErr) {
		return nil, destinationErr
	}
	if prior == nil {
		if destinationErr == nil {
			return nil, transfer.ErrDestination
		}
		for _, other := range []string{"checkout-work", "checkout-ready.json", "stage.json", "intent.json"} {
			if _, e := root.Lstat(other); !os.IsNotExist(e) {
				return nil, transfer.ErrDestination
			}
		}
	}
	if err = bindInstallFile(root, "checkout-intent.json", encoded, 16384); err != nil {
		return nil, err
	}
	ready, err := readInstallFile(root, "checkout-ready.json", 16384)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if ready != nil && !bytes.Equal(ready, encoded) {
		return nil, transfer.ErrChanged
	}
	helper, err := os.Executable()
	if err != nil {
		return nil, err
	}
	command := gitcheckout.Command{StateDir: state, Helper: helper, ExtraConfig: a.checkoutGitConfig}
	if a.checkoutRemote != nil {
		remote = a.checkoutRemote(in.Repository)
	}
	if destinationErr == nil {
		// Includes recovery after rename but before writing the ready marker.
		dir, e := support.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
		if e != nil {
			return nil, transfer.ErrDestination
		}
		defer dir.Close()
		info, e := dir.Stat()
		if e != nil || !info.IsDir() || !ownedByUser(info) || info.Mode().Perm()&0077 != 0 || syscall.Flock(int(dir.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
			return nil, transfer.ErrDestination
		}
		command.Directory = destination
		if err = verifyCheckoutConfig(root, destination, false); err != nil {
			return nil, err
		}
		if err = verifyPreparedCheckout(ctx, command, in, remote); err != nil {
			return nil, err
		}
		if err = bindInstallFile(root, "checkout-ready.json", encoded, 16384); err != nil {
			return nil, err
		}
		return receipt, nil
	}
	if ready != nil {
		return nil, transfer.ErrChanged
	}
	// The private scratch is never exposed in a receipt or used by a provider.
	if info, e := root.Lstat("checkout-work"); e == nil {
		if !info.IsDir() || !ownedByUser(info) || info.Mode().Perm()&0077 != 0 {
			return nil, transfer.ErrDestination
		}
		if err = root.RemoveAll("checkout-work"); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(e) {
		return nil, e
	}
	if err = root.Mkdir("checkout-work", 0700); err != nil {
		return nil, err
	}
	command.Directory = filepath.Join(state, "checkout-work")
	run := func(args ...string) ([]byte, error) { return gitcheckout.Run(ctx, command, args...) }
	if _, err = run("init", "--template="); err != nil {
		return nil, err
	}
	if _, err = run("check-ref-format", "refs/heads/"+in.Branch); err != nil {
		return nil, transfer.ErrInput
	}
	if _, err = run("remote", "add", "origin", remote); err != nil {
		return nil, err
	}
	if err = verifyCheckoutConfig(root, command.Directory, true); err != nil {
		return nil, err
	}
	credentialName := "checkout-credential-" + uuid.NewString()
	credentialBytes, _ := json.Marshal(in.Credential)
	f, err := root.OpenFile(credentialName, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	defer root.Remove(credentialName)
	_, err = f.Write(credentialBytes)
	err = errors.Join(err, f.Close())
	if err != nil {
		return nil, err
	}
	command.CredentialFile = filepath.Join(state, credentialName)
	if _, err = run("fetch", "--no-tags", "--depth=1", "origin", in.BaseCommit); err != nil {
		return nil, err
	}
	command.CredentialFile = ""
	if err = root.Remove(credentialName); err != nil {
		return nil, err
	}
	if err = verifyCheckoutConfig(root, command.Directory, false); err != nil {
		return nil, err
	}
	tree, err := run("ls-tree", "-rlz", "--full-tree", in.BaseCommit)
	if err != nil {
		return nil, err
	}
	if err = gitcheckout.ValidateTree(ctx, tree, func(ctx context.Context, id string) ([]byte, error) {
		return gitcheckout.Run(ctx, command, "cat-file", "blob", id)
	}); err != nil {
		return nil, err
	}
	if _, err = run("checkout", "--no-recurse-submodules", "--no-track", "-b", in.Branch, in.BaseCommit); err != nil {
		return nil, err
	}
	if err = verifyPreparedCheckout(ctx, command, in, remote); err != nil {
		return nil, err
	}
	if err = syncPreparedCheckout(ctx, command.Directory); err != nil {
		return nil, err
	}
	from, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer from.Close()
	to, err := support.Open(".")
	if err != nil {
		return nil, err
	}
	defer to.Close()
	if err = publishCheckout(from, to, "checkout-work", name); err != nil {
		return nil, err
	}
	if err = syncInstallDirectory(support); err != nil {
		return nil, err
	}
	if err = syncInstallDirectory(root); err != nil {
		return nil, err
	}
	if err = bindInstallFile(root, "checkout-ready.json", encoded, 16384); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return receipt, nil
}

func verifyPreparedCheckout(ctx context.Context, c gitcheckout.Command, in TransferCheckoutInput, remote string) error {
	for _, check := range []struct {
		args []string
		want string
	}{
		{[]string{"rev-parse", "--verify", "HEAD"}, in.BaseCommit},
		{[]string{"symbolic-ref", "--short", "HEAD"}, in.Branch},
		{[]string{"remote", "get-url", "origin"}, remote},
		{[]string{"status", "--porcelain=v1", "--untracked-files=all"}, ""},
	} {
		out, err := gitcheckout.Run(ctx, c, check.args...)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(out)) != check.want {
			return transfer.ErrChanged
		}
	}
	return nil
}

func cleanCheckoutCredentials(root *os.Root) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	for count := 0; ; {
		entries, err := dir.ReadDir(128)
		if err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for _, entry := range entries {
			count++
			if count > 1024 {
				return transfer.ErrLimit
			}
			id, ok := strings.CutPrefix(entry.Name(), "checkout-credential-")
			if !ok {
				continue
			}
			if !installUUID(id) {
				return transfer.ErrDestination
			}
			info, e := root.Lstat(entry.Name())
			if e != nil {
				return e
			}
			if !info.Mode().IsRegular() || !ownedByUser(info) || info.Mode().Perm()&0077 != 0 || info.Sys().(*syscall.Stat_t).Nlink != 1 {
				return transfer.ErrDestination
			}
			if e = root.Remove(entry.Name()); e != nil {
				return e
			}
		}
		if errors.Is(err, io.EOF) {
			return syncInstallDirectory(root)
		}
	}
}

func syncPreparedCheckout(ctx context.Context, path string) error {
	count := 0
	return filepath.WalkDir(path, func(name string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		count++
		if count > 60000 {
			return transfer.ErrLimit
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return transfer.ErrDestination
		}
		f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
		if err != nil {
			return err
		}
		return errors.Join(f.Sync(), f.Close())
	})
}

// The private state directory is outside the provider's checkout. Refuse a
// changed local Git configuration before Git can invoke workspace-owned filters.
func verifyCheckoutConfig(state *os.Root, destination string, initial bool) error {
	path := filepath.Join(destination, ".git")
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || !ownedByUser(info) {
		return transfer.ErrDestination
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	f, err := root.OpenFile("config", os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return transfer.ErrDestination
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() || !ownedByUser(info) || info.Size() > 65536 || info.Sys().(*syscall.Stat_t).Nlink != 1 {
		return transfer.ErrDestination
	}
	raw, err := io.ReadAll(io.LimitReader(f, 65537))
	if err != nil || len(raw) > 65536 {
		return transfer.ErrDestination
	}
	hash := []byte(installHash(raw))
	if initial {
		return bindInstallFile(state, "checkout-config.sha256", hash, 64)
	}
	saved, err := readInstallFile(state, "checkout-config.sha256", 64)
	if err != nil || !bytes.Equal(saved, hash) {
		return transfer.ErrChanged
	}
	return nil
}
