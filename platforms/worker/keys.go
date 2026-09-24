package worker

// DocumentKey is the storage key that holds document name's metadata. Its
// presence tells the JavaScript shim, without booting the runtime, that the
// object has written that document.
func DocumentKey(name string) string { return metaKey(documentsNamespace, name) }
