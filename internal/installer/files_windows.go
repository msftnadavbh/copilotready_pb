//go:build windows

package installer

import (
	"os"
	"syscall"
	"unsafe"
)

var kernel = syscall.NewLazyDLL("kernel32.dll")

func linked(info os.FileInfo) bool {
	if info.Mode()&os.ModeSymlink != 0 {
		return true
	}
	if stat, ok := info.Sys().(*syscall.Win32FileAttributeData); ok {
		return stat.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return false
}
func lockFile(f *os.File) error {
	var over syscall.Overlapped
	r, _, e := kernel.NewProc("LockFileEx").Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&over)))
	if r == 0 {
		return e
	}
	return nil
}
func unlockFile(f *os.File) {
	var over syscall.Overlapped
	kernel.NewProc("UnlockFileEx").Call(f.Fd(), 0, 1, 0, uintptr(unsafe.Pointer(&over)))
}
func replaceFile(from, to string) error {
	a, e := syscall.UTF16PtrFromString(from)
	if e != nil {
		return e
	}
	b, e := syscall.UTF16PtrFromString(to)
	if e != nil {
		return e
	}
	r, _, e := kernel.NewProc("MoveFileExW").Call(uintptr(unsafe.Pointer(a)), uintptr(unsafe.Pointer(b)), 9)
	if r == 0 {
		return e
	}
	return nil
}
