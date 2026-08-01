//go:build linux

package upgrader

import "syscall"

func setSysProcAttr() any {
	return &syscall.SysProcAttr{Setpgid: true}
}
