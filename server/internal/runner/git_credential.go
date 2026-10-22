package runner

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

type gitCredential struct {
	URL       string    `json:"url"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Git invokes this fixed helper before HTTPS fetch. It never saves credentials
// and refuses every destination except the exact authorized GitHub repository.
func GitCredential(action, file string, input io.Reader, output io.Writer) error {
	if action == "store" || action == "erase" {
		return nil
	}
	if action != "get" {
		return ErrRejected
	}
	raw, err := privateFile(file, 4096)
	if err != nil {
		return ErrRejected
	}
	var credential gitCredential
	if json.Unmarshal(raw, &credential) != nil || !credential.ExpiresAt.After(time.Now()) || !validGitToken(credential.Token) {
		return ErrRejected
	}
	u, err := url.Parse(credential.URL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !validRepository(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")) {
		return ErrRejected
	}
	scanner := bufio.NewScanner(io.LimitReader(input, 8193))
	scanner.Buffer(make([]byte, 1024), 8192)
	fields := map[string]string{}
	size := 0
	for scanner.Scan() {
		line := scanner.Text()
		size += len(line) + 1
		if size > 8192 {
			return ErrRejected
		}
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return ErrRejected
		}
		if _, duplicate := fields[key]; duplicate {
			return ErrRejected
		}
		fields[key] = value
	}
	if scanner.Err() != nil || fields["protocol"] != "https" || fields["host"] != "github.com" || fields["path"] != strings.TrimPrefix(u.Path, "/") {
		return ErrRejected
	}
	_, err = fmt.Fprintf(output, "username=x-access-token\npassword=%s\n\n", credential.Token)
	return err
}

func validGitToken(value string) bool {
	return strings.HasPrefix(value, "ghu_") && len(value) >= 12 && len(value) <= 512 && !strings.ContainsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	})
}
