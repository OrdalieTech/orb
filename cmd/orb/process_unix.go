//go:build !windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/OrdalieTech/orb/agent/config"
	"golang.org/x/sys/unix"
)

func detachedDaemonProcAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

func execReplacingProcess(path string, argv, environment []string) error {
	return unix.Exec(path, argv, environment)
}

func requireOfflineMigration(ctx context.Context, agentDir string) error {
	output, err := exec.CommandContext(ctx, "ps", "-axo", "uid=,pid=,comm=").Output()
	if err != nil {
		return errors.New("cannot verify that legacy Orb writers are stopped")
	}
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		uid, _ := strconv.Atoi(fields[0])
		pid, _ := strconv.Atoi(fields[1])
		name := filepath.Base(strings.Join(fields[2:], " "))
		if uid == os.Getuid() && pid != os.Getpid() && (name == "orb" || strings.HasPrefix(name, "orb-")) {
			environment, probeErr := exec.CommandContext(ctx, "ps", "eww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
			if probeErr != nil {
				if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
					continue
				}
				return errors.New("cannot inspect a running Orb process before migration")
			}
			configured := os.Getenv(config.EnvAgentDir)
			if configured != "" && !strings.Contains(string(environment), config.EnvAgentDir+"="+configured+" ") && !strings.HasSuffix(strings.TrimSpace(string(environment)), config.EnvAgentDir+"="+configured) {
				continue
			}
			if configured == "" && strings.Contains(string(environment), config.EnvAgentDir+"=") {
				continue
			}
			return fmt.Errorf("close other Orb processes before migration (process %d is still running)", pid)
		}
	}
	return nil
}
