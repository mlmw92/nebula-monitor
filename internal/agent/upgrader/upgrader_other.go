//go:build !linux

package upgrader

func setSysProcAttr() any { return nil }
