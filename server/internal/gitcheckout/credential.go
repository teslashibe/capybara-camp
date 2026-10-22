package gitcheckout

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"syscall"
	"time"
)

type Credential struct {
	URL       string    `json:"url"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Git invokes this fixed helper before HTTPS fetch. It never saves credentials
// and refuses every destination except the exact authorized GitHub repository.
func ServeCredential(action, file string, input io.Reader, output io.Writer) error {
	if action == "store" || action == "erase" {
		return nil
	}
	if action != "get" {
		return ErrState
	}
	raw, err := privateFile(file, 4096)
	if err != nil {
		return ErrState
	}
	var credential Credential
	if json.Unmarshal(raw, &credential) != nil || !credential.ExpiresAt.After(time.Now()) || !ValidToken(credential.Token) {
		return ErrState
	}
	u, err := url.Parse(credential.URL)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !ValidRepository(strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")) {
		return ErrState
	}
	scanner := bufio.NewScanner(io.LimitReader(input, 8193))
	scanner.Buffer(make([]byte, 1024), 8192)
	fields := map[string]string{}
	size := 0
	for scanner.Scan() {
		line := scanner.Text()
		size += len(line) + 1
		if size > 8192 {
			return ErrState
		}
		if line == "" {
			break
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			return ErrState
		}
		if _, duplicate := fields[key]; duplicate {
			return ErrState
		}
		fields[key] = value
	}
	if scanner.Err() != nil || fields["protocol"] != "https" || fields["host"] != "github.com" || fields["path"] != strings.TrimPrefix(u.Path, "/") {
		return ErrState
	}
	_, err = fmt.Fprintf(output, "username=x-access-token\npassword=%s\n\n", credential.Token)
	return err
}

func ValidToken(value string) bool {
	return strings.HasPrefix(value, "ghu_") && len(value) >= 12 && len(value) <= 512 && !strings.ContainsFunc(value, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_')
	})
}

func privateFile(path string, limit int64) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, ErrState
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, ErrState
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || int(stat.Uid) != os.Geteuid() || stat.Nlink != 1 || info.Size() > limit {
		return nil, ErrState
	}
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, ErrState
	}
	return raw, nil
}
