package gitcheckout

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func treeFixture(entries map[string]string) ([]byte, func(context.Context, string) ([]byte, error)) {
	blobs := map[string][]byte{}
	var records []string
	for name, target := range entries {
		sum := sha1.Sum([]byte(name + target))
		id := hex.EncodeToString(sum[:])
		mode := "100644"
		if target != "" {
			mode = "120000"
			blobs[id] = []byte(target)
		}
		records = append(records, fmt.Sprintf("%s blob %s %d\t%s", mode, id, len(target), name))
	}
	return []byte(strings.Join(records, "\x00")), func(_ context.Context, id string) ([]byte, error) { return blobs[id], nil }
}

func TestValidateTreeFollowsCaseAliasedLinks(t *testing.T) {
	// On a case-insensitive filesystem x resolves through a/b/c/l to e, and its
	// four parent steps then leave the checkout.
	data, blob := treeFixture(map[string]string{
		"a/b/c/l": "../../../e",
		"x":       "A/B/C/L/../../../../escaped-target",
		"e/file":  "",
	})
	if err := ValidateTree(context.Background(), data, blob); !errors.Is(err, ErrUnsafe) {
		t.Fatal("case-aliased symlink escape was accepted", err)
	}
	safe, safeBlob := treeFixture(map[string]string{
		"a/b/c/l": "../../../e",
		"x":       "A/B/C/L/file",
		"e/file":  "",
	})
	if err := ValidateTree(context.Background(), safe, safeBlob); err != nil {
		t.Fatal("contained case-aliased link was refused", err)
	}
}
