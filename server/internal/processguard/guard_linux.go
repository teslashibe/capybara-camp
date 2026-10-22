//go:build linux

package processguard

import (
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const minimumABI = 6
const readAccess = unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR | unix.LANDLOCK_ACCESS_FS_EXECUTE
const writeAccess = unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_REMOVE_DIR | unix.LANDLOCK_ACCESS_FS_REMOVE_FILE | unix.LANDLOCK_ACCESS_FS_MAKE_REG | unix.LANDLOCK_ACCESS_FS_MAKE_DIR | unix.LANDLOCK_ACCESS_FS_MAKE_SYM | unix.LANDLOCK_ACCESS_FS_MAKE_FIFO | unix.LANDLOCK_ACCESS_FS_MAKE_SOCK | unix.LANDLOCK_ACCESS_FS_REFER | unix.LANDLOCK_ACCESS_FS_TRUNCATE
const handledAccess = readAccess | writeAccess | unix.LANDLOCK_ACCESS_FS_MAKE_CHAR | unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV

func abiVersion() (int, error) {
	version, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 || version < minimumABI {
		return 0, ErrUnavailable
	}
	return int(version), nil
}

func Available() error { _, err := abiVersion(); return err }
func addPath(fd int, path string, access uint64, optional bool) error {
	target, err := unix.Open(path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		if optional && err == unix.ENOENT {
			return nil
		}
		return ErrPolicy
	}
	defer unix.Close(target)
	var stat unix.Stat_t
	if unix.Fstat(target, &stat) != nil {
		return ErrPolicy
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		access &= unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	rule := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(target)}
	_, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(fd), unix.LANDLOCK_RULE_PATH_BENEATH, uintptr(unsafe.Pointer(&rule)), 0, 0, 0)
	runtime.KeepAlive(rule)
	if errno != 0 {
		return ErrUnavailable
	}
	return nil
}
func restrictAndExec(p Policy, args []string) error {
	if _, err := abiVersion(); err != nil {
		return err
	}
	// Restrictions apply to this OS thread and to all children of the subsequent
	// exec. No untrusted code runs in the wrapper's other Go runtime threads.
	runtime.LockOSThread()
	attr := unix.LandlockRulesetAttr{Access_fs: handledAccess, Scoped: unix.LANDLOCK_SCOPE_SIGNAL | unix.LANDLOCK_SCOPE_ABSTRACT_UNIX_SOCKET}
	result, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	runtime.KeepAlive(attr)
	if errno != 0 {
		return ErrUnavailable
	}
	fd := int(result)
	defer unix.Close(fd)
	unix.CloseOnExec(fd)
	// Installed executables, shared libraries and CA/DNS configuration are read-only.
	// /proc is read-only for runtime maps/cgroup discovery. Landlock's ptrace
	// checks deny sensitive files of the parent domain, including its environ,
	// memory and descriptors. A proc root/fd symlink cannot bypass path rules.
	// Never grant /run, /tmp, /home, /auth, /state or the workspace parent.
	for _, path := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/opt/camp/providers", "/proc", "/sys/kernel/mm/transparent_hugepage/enabled", "/sys/devices/system/cpu/online", "/etc/ld.so.cache", "/etc/ssl", "/etc/pki", "/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/passwd", "/etc/group", "/etc/localtime"} {
		if err := addPath(fd, path, readAccess, true); err != nil {
			return err
		}
	}
	for _, path := range []string{"/dev/null", "/dev/zero", "/dev/urandom", "/dev/random"} {
		if err := addPath(fd, path, unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_WRITE_FILE|unix.LANDLOCK_ACCESS_FS_IOCTL_DEV, false); err != nil {
			return err
		}
	}
	self, err := os.Executable()
	if err != nil {
		return ErrUnavailable
	}
	if err = addPath(fd, self, readAccess, false); err != nil {
		return err
	}
	for _, file := range p.ReadFiles {
		if err = addPath(fd, file, unix.LANDLOCK_ACCESS_FS_READ_FILE, false); err != nil {
			return err
		}
	}
	if err := addPath(fd, p.Home, readAccess|writeAccess, false); err != nil {
		return err
	}
	access := uint64(readAccess)
	if p.WriteWorkspace {
		access |= writeAccess
	}
	if err := addPath(fd, p.Workspace, access, false); err != nil {
		return err
	}
	if unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0) != nil {
		return ErrUnavailable
	}
	_, _, errno = unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(fd), 0, 0)
	if errno != 0 {
		return ErrUnavailable
	}
	if unix.Exec(args[0], args, os.Environ()) != nil {
		return ErrUnavailable
	}
	return nil
}
