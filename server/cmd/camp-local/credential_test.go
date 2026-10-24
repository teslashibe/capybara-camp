package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/teslashibe/capybara-camp/server/internal/gitcheckout"
)

func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "git-credential" {
		main()
		return
	}
	os.Exit(m.Run())
}

func TestBundledCredentialEntrypoint(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "credential")
	token := "ghu_" + strings.Repeat("n", 36)
	data, _ := json.Marshal(gitcheckout.Credential{URL: "https://github.com/fixture/repo.git", Token: token, ExpiresAt: time.Now().Add(time.Minute)})
	if err = os.WriteFile(file, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"allowed", "wrong-repo", "wrong-host", "store", "public", "hardlink", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			action := "get"
			input := "protocol=https\nhost=github.com\npath=fixture/repo.git\n\n"
			path := file
			switch kind {
			case "wrong-repo":
				input = strings.Replace(input, "fixture/repo.git", "fixture/other.git", 1)
			case "wrong-host":
				input = strings.Replace(input, "github.com", "elsewhere.example", 1)
			case "store":
				action = "store"
			case "public":
				if err = os.Chmod(file, 0644); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(file, 0600)
			case "hardlink":
				link := filepath.Join(t.TempDir(), "linked")
				if err = os.Link(file, link); err != nil {
					t.Fatal(err)
				}
				defer os.Remove(link)
			case "symlink":
				path = filepath.Join(t.TempDir(), "linked")
				if err = os.Symlink(file, path); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(self, "git-credential", action)
			cmd.Env = []string{"CAMP_GIT_CREDENTIAL_FILE=" + path}
			cmd.Stdin = strings.NewReader(input)
			var out, stderr bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &stderr
			err := cmd.Run()
			if kind == "allowed" {
				if err != nil || out.String() != "username=x-access-token\npassword="+token+"\n\n" {
					t.Fatal("Credential helper failed")
				}
			} else if kind == "store" {
				if err != nil || out.Len() != 0 {
					t.Fatal("Credential helper stored credentials")
				}
			} else if err == nil || out.Len() != 0 || stderr.Len() != 0 {
				t.Fatal("Credential escaped its destination or safe failure")
			}
		})
	}
}
