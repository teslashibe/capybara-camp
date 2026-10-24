package localcli

import (
	"golang.org/x/sys/unix"
	"os"
)

func publishCheckout(from, to *os.File, source, destination string) error {
	return unix.Renameat2(int(from.Fd()), source, int(to.Fd()), destination, unix.RENAME_NOREPLACE)
}
