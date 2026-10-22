package localcli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCredentialSnapshotConfinesFilesAndIdentity(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"version":1,"provider":"Claude","files":{".credentials.json":{"claudeAiOauth":{"accessToken":"synthetic","refreshToken":"synthetic"}},".claude.json":{"oauthAccount":{"accountUuid":"account","organizationUuid":"org"}}}}`)
	subject := "claude:org:account"
	if err := ValidateCredentialSnapshot("Claude", subject, raw); err != nil {
		t.Fatal(err)
	}
	if ValidateCredentialSnapshot("Claude", "claude:org:another", raw) == nil {
		t.Fatal("changed identity accepted")
	}
	var extra map[string]any
	_ = json.Unmarshal(raw, &extra)
	extra["files"].(map[string]any)["../outside"] = map[string]string{}
	invalid, _ := json.Marshal(extra)
	if ValidateCredentialSnapshot("Claude", subject, invalid) == nil {
		t.Fatal("arbitrary snapshot path accepted")
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	if err := RestoreCredentialSnapshot("Claude", subject, root, raw); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(outside); string(data) != "untouched" {
		t.Fatal("credential restore followed symlink")
	}
	if _, err := CaptureCredentialSnapshot("Claude", subject, root); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, ".credentials.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := CaptureCredentialSnapshot("Claude", subject, root); err == nil {
		t.Fatal("credential capture followed symlink")
	}
}
