package localcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/teslashibe/capybara-camp/server/internal/gitcheckout"
	"github.com/teslashibe/capybara-camp/server/internal/testgit"
	"github.com/teslashibe/capybara-camp/server/internal/transfer"
)

func localCheckoutFixture(t *testing.T, seed func(string), before func(http.ResponseWriter, *http.Request) bool) (*App, TransferCheckoutInput, *atomic.Int32) {
	t.Helper()
	remote, sha, count := testgit.Server(t, seed, before)
	a := fixture(t)
	a.checkoutRemote = func(string) string { return remote }
	a.checkoutGitConfig = []string{"-c", "protocol.http.allow=always"}
	in := TransferCheckoutInput{Scope: strings.Repeat("a", 64), TransferID: uuid.NewString(), Repository: "fixture/repo", BaseCommit: sha, Branch: "camp/local-restore", Credential: gitcheckout.Credential{URL: "https://github.com/fixture/repo.git", Token: "ghu_" + strings.Repeat("n", 36), ExpiresAt: time.Now().Add(10 * time.Minute)}}
	return a, in, count
}
func localCheckoutCall(ctx context.Context, a *App, in TransferCheckoutInput) (TransferCheckoutReceipt, error) {
	raw, _ := json.Marshal(in)
	result, err := a.Run(ctx, "transfer-checkout", bytes.NewReader(raw))
	if err != nil {
		return TransferCheckoutReceipt{}, err
	}
	return result.(TransferCheckoutReceipt), nil
}
func assertNoCheckoutCredential(t *testing.T, a *App, in TransferCheckoutInput) {
	t.Helper()
	err := filepath.WalkDir(a.Config.Support, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if strings.HasPrefix(entry.Name(), "checkout-credential-") {
			t.Error("Credential file retained")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte(in.Credential.Token)) {
			t.Error("Token persisted")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestLocalTransferCheckoutPinnedRestartAndIsolation(t *testing.T) {
	a, in, count := localCheckoutFixture(t, func(dir string) {
		if err := os.Symlink("README.md", filepath.Join(dir, "readme-link")); err != nil {
			t.Fatal(err)
		}
		testgit.Command(t, dir, "add", ".")
	}, nil)
	first, err := localCheckoutCall(t.Context(), a, in)
	if err != nil {
		t.Fatal(err)
	}
	if first.BaseCommit != in.BaseCommit || first.Branch != in.Branch || first.Scope != in.Scope || first.TransferID != in.TransferID {
		t.Fatal("Wrong checkout receipt")
	}
	if head := testgit.Command(t, first.Destination, "rev-parse", "HEAD"); head != in.BaseCommit {
		t.Fatal("Wrong commit")
	}
	info, err := os.Stat(first.Destination)
	if err != nil || info.Mode().Perm() != 0700 {
		t.Fatal("Checkout not private", err)
	}
	if link, err := os.Readlink(filepath.Join(first.Destination, "readme-link")); err != nil || link != "README.md" {
		t.Fatal("Safe symlink lost", err)
	}
	requests := count.Load()
	restarted := New(a.Config)
	restarted.checkoutRemote = a.checkoutRemote
	restarted.checkoutGitConfig = a.checkoutGitConfig
	in.Credential.Token = "ghu_" + strings.Repeat("r", 36)
	again, err := localCheckoutCall(t.Context(), restarted, in)
	if err != nil || again != first {
		t.Fatal("Retry did not retain receipt", err)
	}
	if count.Load() != requests {
		t.Fatal("Ready checkout fetched again")
	}
	other := in
	other.TransferID = uuid.NewString()
	second, err := localCheckoutCall(t.Context(), a, other)
	if err != nil || second.Destination == first.Destination {
		t.Fatal("Independent transfer shared checkout", err)
	}
	assertNoCheckoutCredential(t, a, in)
}

func TestLocalTransferCheckoutPreservesPublishedWork(t *testing.T) {
	for _, kind := range []string{"dirty", "untracked", "config", "base", "branch", "repository", "missing", "symlink", "lock", "hardlink-lock"} {
		t.Run(kind, func(t *testing.T) {
			a, in, _ := localCheckoutFixture(t, nil, nil)
			receipt, err := localCheckoutCall(t.Context(), a, in)
			if err != nil {
				t.Fatal(err)
			}
			state := filepath.Join(a.Config.Support, "restore-"+in.Scope+"-"+in.TransferID)
			switch kind {
			case "dirty":
				write(t, filepath.Join(receipt.Destination, "README.md"), "my newer work")
			case "untracked":
				write(t, filepath.Join(receipt.Destination, "new.txt"), "my newer work")
			case "config":
				// Even a local filter command must not run while inspecting a retry.
				testgit.Command(t, receipt.Destination, "config", "filter.unsafe.clean", "touch ../filter-ran")
			case "base":
				in.BaseCommit = strings.Repeat("b", 40)
			case "branch":
				in.Branch = "changed"
			case "repository":
				in.Repository = "fixture/other"
				in.Credential.URL = "https://github.com/fixture/other.git"
			case "missing":
				if err = os.Rename(receipt.Destination, receipt.Destination+"-retained"); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err = os.Rename(receipt.Destination, receipt.Destination+"-retained"); err != nil {
					t.Fatal(err)
				}
				if err = os.Symlink(receipt.Destination+"-retained", receipt.Destination); err != nil {
					t.Fatal(err)
				}
			case "lock":
				f, e := os.Open(filepath.Join(state, "stage.lock"))
				if e != nil {
					t.Fatal(e)
				}
				defer f.Close()
				if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
					t.Fatal(e)
				}
			case "hardlink-lock":
				if err = os.Link(filepath.Join(state, "stage.lock"), filepath.Join(t.TempDir(), "link")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = localCheckoutCall(t.Context(), a, in); err == nil {
				t.Fatal("Accepted changed checkout")
			}
			if kind == "dirty" {
				data, _ := os.ReadFile(filepath.Join(receipt.Destination, "README.md"))
				if string(data) != "my newer work" {
					t.Fatal("Discarded local edits")
				}
			}
			if kind == "untracked" {
				data, _ := os.ReadFile(filepath.Join(receipt.Destination, "new.txt"))
				if string(data) != "my newer work" {
					t.Fatal("Discarded local file")
				}
			}
			if _, err = os.Stat(filepath.Join(a.Config.Support, "filter-ran")); !os.IsNotExist(err) {
				t.Fatal("Ran changed Git config")
			}
		})
	}
}

func TestLocalTransferCheckoutInterruptedFetchAndPublication(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	a, in, count := localCheckoutFixture(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
		if fail.Load() {
			http.Error(w, "fixture failure", 503)
			return true
		}
		return false
	})
	if _, err := localCheckoutCall(t.Context(), a, in); err == nil {
		t.Fatal("Failed fetch succeeded")
	}
	assertNoCheckoutCredential(t, a, in)
	state := filepath.Join(a.Config.Support, "restore-"+in.Scope+"-"+in.TransferID)
	orphan := filepath.Join(state, "checkout-credential-"+uuid.NewString())
	write(t, orphan, in.Credential.Token)
	fail.Store(false)
	receipt, err := localCheckoutCall(t.Context(), a, in)
	if err != nil {
		t.Fatal(err)
	}
	assertNoCheckoutCredential(t, a, in)
	// A crash between atomic rename and receipt persistence must adopt only the
	// verified clean checkout, without another fetch or destructive reset.
	if err = os.Remove(filepath.Join(state, "checkout-ready.json")); err != nil {
		t.Fatal(err)
	}
	before := count.Load()
	recovered, err := localCheckoutCall(t.Context(), a, in)
	if err != nil || recovered != receipt || count.Load() != before {
		t.Fatal("Publication recovery failed", err)
	}
}

func TestLocalTransferCheckoutRejectsUnsafeTrees(t *testing.T) {
	for _, kind := range []string{"escape", "cycle", "gitlink", "case-collision"} {
		t.Run(kind, func(t *testing.T) {
			a, in, _ := localCheckoutFixture(t, func(dir string) {
				switch kind {
				case "escape":
					if err := os.Symlink("../../outside", filepath.Join(dir, "link")); err != nil {
						t.Fatal(err)
					}
				case "cycle":
					if err := os.Symlink("link", filepath.Join(dir, "link")); err != nil {
						t.Fatal(err)
					}
				case "gitlink":
					testgit.Command(t, dir, "update-index", "--add", "--cacheinfo", "160000,"+strings.Repeat("a", 40)+",submodule")
				case "case-collision":
					blob := testgit.Command(t, dir, "hash-object", "README.md")
					testgit.Command(t, dir, "update-index", "--add", "--cacheinfo", "100644,"+blob+",readme.md")
				}
				if kind != "case-collision" && kind != "gitlink" {
					testgit.Command(t, dir, "add", ".")
				}
			}, nil)
			if _, err := localCheckoutCall(t.Context(), a, in); err == nil {
				t.Fatal("Checked out unsafe tree")
			}
			assertNoCheckoutCredential(t, a, in)
		})
	}
}

func TestLocalTransferCheckoutRejectsInputBeforeNetwork(t *testing.T) {
	a, valid, count := localCheckoutFixture(t, nil, nil)
	for _, kind := range []string{"scope", "id", "repo", "commit", "credential-repo", "expired", "long-lived", "branch", "unknown-json"} {
		t.Run(kind, func(t *testing.T) {
			in := valid
			switch kind {
			case "scope":
				in.Scope = "../escape"
			case "id":
				in.TransferID = "../escape"
			case "repo":
				in.Repository = "https://elsewhere.example/repo"
			case "commit":
				in.BaseCommit = "HEAD"
			case "credential-repo":
				in.Credential.URL = "https://github.com/fixture/other.git"
			case "expired":
				in.Credential.ExpiresAt = time.Now().Add(-time.Second)
			case "long-lived":
				in.Credential.ExpiresAt = time.Now().Add(2 * time.Hour)
			case "branch":
				in.Branch = "invalid\nbranch"
			}
			raw, _ := json.Marshal(in)
			if kind == "unknown-json" {
				raw = append([]byte(`{"command":"anything",`), raw[1:]...)
			}
			if _, err := a.Run(t.Context(), "transfer-checkout", bytes.NewReader(raw)); err == nil {
				t.Fatal("Accepted invalid input")
			}
		})
	}
	if count.Load() != 0 {
		t.Fatal("Invalid input used network")
	}
}

func TestLocalTransferCheckoutCancellation(t *testing.T) {
	started := make(chan struct{}, 1)
	a, in, _ := localCheckoutFixture(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
		return true
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := localCheckoutCall(ctx, a, in); done <- err }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("Fetch did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("Cancellation was lost", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Git did not stop")
	}
	assertNoCheckoutCredential(t, a, in)
}

func TestLocalTransferCheckoutFeedsHistoryInstaller(t *testing.T) {
	a, install := installFixture(t, "Claude")
	remote, sha, _ := testgit.Server(t, nil, nil)
	a.checkoutRemote = func(string) string { return remote }
	a.checkoutGitConfig = []string{"-c", "protocol.http.allow=always"}
	checkout := TransferCheckoutInput{Scope: install.Scope, TransferID: install.TransferID, Repository: "fixture/repo", BaseCommit: sha, Branch: "camp/imported", Credential: gitcheckout.Credential{URL: "https://github.com/fixture/repo.git", Token: "ghu_" + strings.Repeat("n", 36), ExpiresAt: time.Now().Add(10 * time.Minute)}}
	ready, err := localCheckoutCall(t.Context(), a, checkout)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(ready.Destination, "README.md"), "reviewed cloud changes\n")
	manifest, err := transfer.Capture(t.Context(), ready.Destination, []string{"README.md"})
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(ready.Destination, "README.md"), "pinned version\n")
	install.Destination = ready.Destination
	install.Branch = ready.Branch
	install.Payload, err = json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	install.PayloadSHA256 = installHash(install.Payload)
	install.ReviewSHA256, err = manifest.Digest()
	if err != nil {
		t.Fatal(err)
	}
	install.Export.SHA256 = install.PayloadSHA256
	install.Export.Bytes = len(install.Payload)
	install.Export.Paths = []string{"README.md"}
	receipt, err := installCall(t.Context(), a, install)
	if err != nil {
		t.Fatal(err)
	}
	restarted, err := installCall(t.Context(), New(a.Config), install)
	if err != nil || restarted != receipt {
		t.Fatal("Prepared checkout did not support installer recovery", err)
	}
	data, _ := os.ReadFile(filepath.Join(ready.Destination, "README.md"))
	if string(data) != "reviewed cloud changes\n" {
		t.Fatal("Reviewed files missing")
	}
	state := installDirectory(a, install)
	history, _ := os.ReadFile(filepath.Join(state, "history.json"))
	if !bytes.Equal(history, install.History) {
		t.Fatal("History missing after checkout")
	}
	if _, err = localCheckoutCall(t.Context(), a, checkout); err == nil {
		t.Fatal("Checkout preparation accepted an already staged destination")
	}
	data, _ = os.ReadFile(filepath.Join(ready.Destination, "README.md"))
	if string(data) != "reviewed cloud changes\n" {
		t.Fatal("Checkout retry discarded staged files")
	}
	assertNoCheckoutCredential(t, a, checkout)
}

func TestLocalTransferCheckoutDoesNotReplaceConcurrentDestination(t *testing.T) {
	var destination string
	var created atomic.Bool
	a, in, _ := localCheckoutFixture(t, nil, func(w http.ResponseWriter, r *http.Request) bool {
		if created.CompareAndSwap(false, true) {
			if err := os.Mkdir(destination, 0700); err != nil {
				t.Error(err)
			}
		}
		return false
	})
	destination = filepath.Join(a.Config.Support, "workspace-"+in.Scope+"-"+in.TransferID)
	if _, err := localCheckoutCall(t.Context(), a, in); err == nil {
		t.Fatal("Replaced a concurrently created directory")
	}
	entries, err := os.ReadDir(destination)
	if err != nil || len(entries) != 0 {
		t.Fatal("Concurrent destination changed", err)
	}
	assertNoCheckoutCredential(t, a, in)
}
