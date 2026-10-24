package gitcheckout

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestGitOutputLimitCannotBeBypassedByCopy(t *testing.T) {
	out := &gitOutput{limit: 8}
	_, err := io.Copy(out, io.LimitReader(strings.NewReader(strings.Repeat("x", 64)), 64))
	if !errors.Is(err, ErrSize) || !out.overflow || out.Len() > 8 {
		t.Fatal("copy bypassed Git output limit", out.Len(), err)
	}
}
