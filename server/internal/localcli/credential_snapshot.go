package localcli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

type credentialSnapshot struct {
	Version  int                        `json:"version"`
	Provider string                     `json:"provider"`
	Files    map[string]json.RawMessage `json:"files"`
}

func snapshotSubject(value credentialSnapshot) (string, error) {
	if value.Version != 1 {
		return "", ErrProviderLogin
	}
	switch value.Provider {
	case "Codex":
		if len(value.Files) != 1 {
			return "", ErrProviderLogin
		}
		return codexLoginSubject(value.Files["auth.json"])
	case "Claude":
		if len(value.Files) != 2 {
			return "", ErrProviderLogin
		}
		var credentials, profile map[string]any
		if json.Unmarshal(value.Files[".credentials.json"], &credentials) != nil || json.Unmarshal(value.Files[".claude.json"], &profile) != nil {
			return "", ErrProviderLogin
		}
		account := object(profile["oauthAccount"])
		oauth := object(credentials["claudeAiOauth"])
		id, org := str(account["accountUuid"]), str(account["organizationUuid"])
		if !identityPart(id) || !identityPart(org) || str(oauth["accessToken"]) == "" || str(oauth["refreshToken"]) == "" {
			return "", ErrProviderLogin
		}
		return "claude:" + org + ":" + id, nil
	default:
		return "", ErrProviderLogin
	}
}

// ValidateCredentialSnapshot checks the fixed snapshot format and previously
// verified identity. It does not authenticate tokens or accept an API key.
func ValidateCredentialSnapshot(provider, subject string, raw []byte) error {
	if len(raw) == 0 || len(raw) > 256<<10 {
		return ErrProviderLogin
	}
	var value credentialSnapshot
	if json.Unmarshal(raw, &value) != nil || value.Provider != provider {
		return ErrProviderLogin
	}
	got, err := snapshotSubject(value)
	if err != nil || got != subject {
		return ErrProviderLogin
	}
	return nil
}

func privateSnapshotRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || !ownedByUser(info) || info.Mode().Perm()&0077 != 0 {
		return errors.New("Provider home must be a private owned directory")
	}
	return nil
}

// RestoreCredentialSnapshot replaces only fixed credential files. Session
// history and other provider files in this private home remain untouched.
func RestoreCredentialSnapshot(provider, subject, root string, raw []byte) error {
	if err := ValidateCredentialSnapshot(provider, subject, raw); err != nil {
		return err
	}
	if err := privateSnapshotRoot(root); err != nil {
		return err
	}
	var value credentialSnapshot
	_ = json.Unmarshal(raw, &value)
	files := value.Files
	for name, data := range files {
		tmp := filepath.Join(root, ".camp-credential-"+uuid.NewString())
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return ErrProviderLogin
		}
		_, err = f.Write(data)
		if err == nil {
			err = f.Sync()
		}
		closed := f.Close()
		if err != nil || closed != nil {
			_ = os.Remove(tmp)
			return ErrProviderLogin
		}
		if err = os.Rename(tmp, filepath.Join(root, name)); err != nil {
			_ = os.Remove(tmp)
			return ErrProviderLogin
		}
	}
	dir, err := os.Open(root)
	if err != nil {
		return ErrProviderLogin
	}
	defer dir.Close()
	if dir.Sync() != nil {
		return ErrProviderLogin
	}
	return nil
}

func CaptureCredentialSnapshot(provider, subject, root string) (json.RawMessage, error) {
	if err := privateSnapshotRoot(root); err != nil {
		return nil, err
	}
	names := []string{"auth.json"}
	if provider == "Claude" {
		names = []string{".credentials.json", ".claude.json"}
	}
	value := credentialSnapshot{Version: 1, Provider: provider, Files: map[string]json.RawMessage{}}
	for _, name := range names {
		data, err := privateLoginFile(root, name)
		if err != nil {
			return nil, err
		}
		value.Files[name] = data
	}
	if provider == "Claude" {
		var profile map[string]any
		if json.Unmarshal(value.Files[".claude.json"], &profile) != nil {
			return nil, ErrProviderLogin
		}
		value.Files[".claude.json"], _ = json.Marshal(map[string]any{"oauthAccount": profile["oauthAccount"]})
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, ErrProviderLogin
	}
	return raw, ValidateCredentialSnapshot(provider, subject, raw)
}

// IsolatedEnvironment excludes host credentials and uses the installation's
// private provider home. It is shared with the sign-in adapter's allowlist.
func IsolatedEnvironment(source []string, home, root, provider string) []string {
	return loginEnvironment(source, home, root, provider)
}
