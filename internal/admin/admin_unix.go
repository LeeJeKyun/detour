//go:build darwin || linux

package admin

import "os"

func IsElevated() bool {
	return os.Geteuid() == 0
}
