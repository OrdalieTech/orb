package auth

import (
	"context"
	"os"
	"strings"
)

type EnvironmentContext struct{}

func (EnvironmentContext) Env(_ context.Context, name string) (string, bool) {
	value, exists := os.LookupEnv(name)
	if !exists || strings.TrimSpace(value) == "" {
		return "", false
	}
	return value, true
}

func (EnvironmentContext) FileExists(_ context.Context, path string) bool {
	if strings.HasPrefix(path, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false
		}
		path = home + strings.TrimPrefix(path, "~")
	}
	_, err := os.Stat(path)
	return err == nil
}
