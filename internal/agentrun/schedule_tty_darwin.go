//go:build darwin

package agentrun

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminal reports whether f is a terminal: the terminal ioctl answers. A
// character device such as /dev/null is not one.
func isTerminal(f *os.File) bool {
	var t syscall.Termios
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), syscall.TIOCGETA, uintptr(unsafe.Pointer(&t)))
	return errno == 0
}
