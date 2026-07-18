package config

import "os"

func environmentValue(name string) string { return os.Getenv(name) }
