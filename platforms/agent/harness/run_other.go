//go:build !linux

package main

import "errors"

func run([]string) (int, error) {
	return 0, errors.New("runs the agent in its Linux image only; check <file> works anywhere")
}
