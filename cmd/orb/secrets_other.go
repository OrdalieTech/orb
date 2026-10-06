//go:build !unix

package main

import "github.com/OrdalieTech/orb/internal/document"

func loadSecrets() error { return nil }

var authDescriptor = func() document.Document { return nil }
