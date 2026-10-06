//go:build !unix

package main

import "github.com/OrdalieTech/orb/internal/document"

func loadSecrets() error { return nil }

func authDescriptor() document.Document { return nil }
