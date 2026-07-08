//go:build windows

package nattraversal

import "syscall"

func setSocketReuseAddr(fd uintptr) error {
	return syscall.SetsockoptInt(syscall.Handle(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
}
