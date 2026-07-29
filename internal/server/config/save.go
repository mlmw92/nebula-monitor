package config

import (
	"os"
	"path/filepath"
)

// AtomicWrite 将 data 原子写入 path：先写临时文件再 rename，避免写入中途进程崩溃导致配置损坏。
func AtomicWrite(path string, data []byte) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
