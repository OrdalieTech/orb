//go:build unix

package jsbridge

import "os"

func processUID() (int, bool) {
	return os.Getuid(), true
}
