package main

import (
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/OrdalieTech/orb/chat/platforms"
)

// plain is the environment the agent gets as environment; everything else,
// secrets included, reaches it as KEY=VALUE lines on ORB_SECRETS_FD, which it
// loads into an environment its tools cannot read.
var plain = regexp.MustCompile(`^(PATH|HOME|TERM|LANG|LC_[A-Z]+|TZ|ORB_[A-Z_]+|PI_[A-Z_]+)=`)

// process is a command, its working directory and environment, and for the
// agent the lines it reads on its secrets descriptor.
type process struct {
	argv, env []string
	dir       string
	secrets   string
}

// agentProcess is `orb chat` with args, run from environ (the container's)
// plus rendered (the agent file's platform settings). It serves its ACP
// socket when a sidecar needs it.
func agentProcess(args, environ []string, rendered map[string]string, image layout, sidecars bool) process {
	agent := process{argv: append([]string{"orb", "chat"}, args...), dir: image.workspace, env: []string{"ORB_SECRETS_FD=3", "ORB_AUTH_FD=4"}}
	var secrets strings.Builder
	for _, entry := range environ {
		if plain.MatchString(entry) {
			agent.env = append(agent.env, entry)
		} else {
			secrets.WriteString(entry + "\n")
		}
	}
	for _, name := range slices.Sorted(maps.Keys(rendered)) {
		secrets.WriteString(name + "=" + rendered[name] + "\n")
	}
	if sidecars {
		agent.env = append(agent.env, "ORB_ACP_SOCKET="+image.socket)
	}
	agent.secrets = secrets.String()
	return agent
}

// sidecarProcess is a platform's sidecar, run in the agent's workspace with
// home as its home: it gets the variables its platform declares, from the
// container and the agent file, then its own settings.
func sidecarProcess(platform platforms.Platform, environ []string, rendered map[string]string, image layout, home string) process {
	argv, settings := platform.Sidecar(image.relay)
	env := []string{"PATH=" + platforms.Env(environ).Get("PATH"), "HOME=" + home}
	env = append(env, platforms.Env(environ).Matching(platform.Env...)...)
	for _, name := range slices.Sorted(maps.Keys(rendered)) {
		if platforms.Matches(name, platform.Env) {
			env = append(env, name+"="+rendered[name])
		}
	}
	return process{argv: argv, dir: image.workspace, env: append(env, settings...)}
}
