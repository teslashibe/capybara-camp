package runner

import (
	"github.com/teslashibe/capybara-camp/server/internal/gitcheckout"
	"io"
)

type gitCredential = gitcheckout.Credential

func validGitToken(value string) bool { return gitcheckout.ValidToken(value) }
func GitCredential(action, file string, input io.Reader, output io.Writer) error {
	if err := gitcheckout.ServeCredential(action, file, input, output); err != nil {
		return ErrRejected
	}
	return nil
}
