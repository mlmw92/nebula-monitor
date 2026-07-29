//go:build linux

package upgrader

import "syscall"

func setSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}
