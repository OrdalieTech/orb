//go:build !windows

package main

import (
	"bytes"
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

// procfs is replaced in tests to exercise the ps path on Linux.
var procfs = "/proc"

func requireOfflineMigration(ctx context.Context, agentDir string) error {
	processes, probe := orbProcessesFromPS, processEnvironmentFromPS
	// Slim container images ship no ps; Linux exposes the same facts in procfs.
	if _, err := os.Stat(filepath.Join(procfs, "self", "environ")); err == nil {
		processes, probe = orbProcessesFromProcfs, processEnvironmentFromProcfs
	}
	pids, err := processes(ctx)
	if err != nil {
		return errors.New("cannot verify that legacy Orb writers are stopped")
	}
	for _, pid := range pids {
		environment, probeErr := probe(ctx, pid)
		if probeErr != nil {
			if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
				continue
			}
			return errors.New("cannot inspect a running Orb process before migration")
		}
		configured := os.Getenv(config.EnvAgentDir)
		if configured != "" && !strings.Contains(environment, config.EnvAgentDir+"="+configured+" ") && !strings.HasSuffix(strings.TrimSpace(environment), config.EnvAgentDir+"="+configured) {
			continue
		}
		if configured == "" && strings.Contains(environment, config.EnvAgentDir+"=") {
			continue
		}
		return fmt.Errorf("close other Orb processes before migration (process %d is still running)", pid)
	}
	return nil
}

func isOrbProcess(uid, pid int, command string) bool {
	name := filepath.Base(command)
	return uid == os.Getuid() && pid != os.Getpid() && (name == "orb" || strings.HasPrefix(name, "orb-"))
}

func orbProcessesFromPS(ctx context.Context) ([]int, error) {
	output, err := exec.CommandContext(ctx, "ps", "-axo", "uid=,pid=,comm=").Output()
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, line := range strings.Split(string(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		uid, _ := strconv.Atoi(fields[0])
		pid, _ := strconv.Atoi(fields[1])
		if isOrbProcess(uid, pid, strings.Join(fields[2:], " ")) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func processEnvironmentFromPS(ctx context.Context, pid int) (string, error) {
	output, err := exec.CommandContext(ctx, "ps", "eww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	return string(output), err
}

func orbProcessesFromProcfs(context.Context) ([]int, error) {
	entries, err := os.ReadDir(procfs)
	if err != nil {
		return nil, err
	}
	var pids []int
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		// Like ps uid=, the owner of /proc/<pid> is the process's effective user.
		info, statErr := os.Stat(filepath.Join(procfs, entry.Name()))
		command, readErr := os.ReadFile(filepath.Join(procfs, entry.Name(), "comm"))
		status, statusErr := os.ReadFile(filepath.Join(procfs, entry.Name(), "stat"))
		if statErr != nil || readErr != nil || statusErr != nil {
			continue // The process exited during the scan.
		}
		// A zombie writes nothing and hides its environment; ps lists it as <defunct>.
		if end := bytes.LastIndexByte(status, ')'); end < 0 || end+2 >= len(status) || status[end+2] == 'Z' || status[end+2] == 'X' {
			continue
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if ok && isOrbProcess(int(stat.Uid), pid, strings.TrimSpace(string(command))) {
			pids = append(pids, pid)
		}
	}
	return pids, nil
}

func processEnvironmentFromProcfs(_ context.Context, pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join(procfs, strconv.Itoa(pid), "environ"))
	return strings.ReplaceAll(string(data), "\x00", " "), err
}
