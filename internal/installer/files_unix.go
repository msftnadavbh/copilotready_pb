//go:build !windows

package installer

import (
	"os"
	"syscall"
)

func linked(info os.FileInfo) bool      { return info.Mode()&os.ModeSymlink != 0 }
func lockFile(f *os.File) error         { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
func unlockFile(f *os.File)             { syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }
func replaceFile(from, to string) error { return os.Rename(from, to) }
