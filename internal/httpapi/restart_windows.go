//go:build windows

package httpapi

import "errors"

// restartProcess Windows 下不支持进程自替换，需手动重启
func restartProcess() error {
	return errors.New("windows 下不支持自动重启，请手动重启 GBServer")
}
