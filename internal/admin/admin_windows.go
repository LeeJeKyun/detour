//go:build windows

package admin

import "golang.org/x/sys/windows"

func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}
