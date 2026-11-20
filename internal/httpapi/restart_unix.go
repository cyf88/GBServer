//go:build !windows

package httpapi

import (
	"os"
	"syscall"
)

// restartProcess 用当前可执行文件原地替换进程（Linux / WSL）
func restartProcess() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return syscall.Exec(exe, os.Args, os.Environ())
}
