//go:build windows

package wcdb

import "syscall"

func loadLibrary(path string) (uintptr, error) {
	h, err := syscall.LoadLibrary(path)
	return uintptr(h), err
}
