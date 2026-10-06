//go:build !unix

package teamenv

import "github.com/OrdalieTech/orb/host"

func LoadSecrets() error { return nil }

func AuthDocument() host.Document { return nil }
