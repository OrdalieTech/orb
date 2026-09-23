package filelock

import (
	"errors"

	"golang.org/x/sys/windows"
)

// deletePending reports the transient errors Windows returns while another
// handle (a contending stat, antivirus, the indexer) keeps a just-removed lock
// directory in "delete pending" state or briefly open: mkdir, stat and rmdir
// fail with access denied or a sharing violation until that handle closes.
func deletePending(err error) bool {
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, windows.ERROR_SHARING_VIOLATION)
}
