//go:build !linux && !darwin

package sandbox

import "fmt"

func Probe() (int, Enforcement, error) { return 0, EnforcementNone, nil }
func wrap(_ Mode, _ string, _ string, _ string, command string, env map[string]string) (string, map[string]string, Enforcement) {
	return command, env, EnforcementNone
}
func SelfRestrict(Mode, string) (Enforcement, error) {
	return EnforcementNone, fmt.Errorf("sandbox: SelfRestrict is only available on Linux")
}
