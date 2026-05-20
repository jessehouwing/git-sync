//go:build windows

package auth

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

var (
	modkernel32      = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = modkernel32.NewProc("LockFileEx")
	procUnlockFileEx = modkernel32.NewProc("UnlockFileEx")
)

const lockfileExclusiveLock = 0x00000002

// flockShared acquires a shared (read) lock on path+".lock".
func flockShared(path string) (func(), error) {
	return flockOpen(path+".lock", 0)
}

// flockExclusive acquires an exclusive (write) lock on path+".lock".
func flockExclusive(path string) (func(), error) {
	return flockOpen(path+".lock", lockfileExclusiveLock)
}

func flockOpen(lockPath string, flags uint32) (func(), error) {
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	handle := syscall.Handle(f.Fd())
	ol := new(syscall.Overlapped)
	if err := lockFileEx(handle, flags, 0, 1, 0, ol); err != nil {
		f.Close()
		return nil, fmt.Errorf("acquire file lock: %w", err)
	}
	return func() {
		//nolint:errcheck // unlock errors on close are not actionable
		unlockFileEx(handle, 0, 1, 0, ol)
		f.Close()
	}, nil
}

func lockFileEx(handle syscall.Handle, flags, reserved, lockLow, lockHigh uint32, ol *syscall.Overlapped) error {
	//nolint:gosec // unsafe.Pointer required for syscall interop with OVERLAPPED struct
	r1, _, err := procLockFileEx.Call(
		uintptr(handle),
		uintptr(flags),
		uintptr(reserved),
		uintptr(lockLow),
		uintptr(lockHigh),
		uintptr(unsafe.Pointer(ol)),
	)
	if r1 == 0 {
		return fmt.Errorf("LockFileEx: %w", err)
	}
	return nil
}

func unlockFileEx(handle syscall.Handle, reserved, lockLow, lockHigh uint32, ol *syscall.Overlapped) error {
	//nolint:gosec // unsafe.Pointer required for syscall interop with OVERLAPPED struct
	r1, _, err := procUnlockFileEx.Call(
		uintptr(handle),
		uintptr(reserved),
		uintptr(lockLow),
		uintptr(lockHigh),
		uintptr(unsafe.Pointer(ol)),
	)
	if r1 == 0 {
		return fmt.Errorf("UnlockFileEx: %w", err)
	}
	return nil
}
