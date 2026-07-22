//go:build !unix

package jsbridge

func processUID() (int, bool) {
	return 0, false
}
