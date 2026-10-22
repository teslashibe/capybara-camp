//go:build !linux

package processguard

func Available() error                       { return ErrUnavailable }
func restrictAndExec(Policy, []string) error { return ErrUnavailable }
