package api

import "os"

// readHostFile is the package's one read of a host file (credential and
// identity-token files named by provider configuration).
var readHostFile = os.ReadFile
