//go:build !linux && !darwin

package sandbox

import "fmt"

func wrap(Mode, string, string, string, env map[string]string) (string, map[string]string) {
	return refuse("unsupported platform"), env
}

func SelfRestrict(Mode, string) error {
	return fmt.Errorf("sandbox: SelfRestrict requires Linux")
}
