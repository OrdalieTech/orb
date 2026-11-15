package main

import (
	"maps"
	"regexp"
	"slices"
	"strings"
)

// plain is the environment the agent gets as environment; everything else,
// secrets included, reaches it as KEY=VALUE lines on ORB_SECRETS_FD, which it
// loads into an environment its tools cannot read.
var plain = regexp.MustCompile(`^(PATH|HOME|TERM|LANG|LC_[A-Z]+|TZ|ORB_[A-Z_]+)=`)

// process is a command, its working directory and environment, and for the
// agent the lines it reads on its secrets descriptor.
type process struct {
	argv, env []string
	dir       string
	secrets   string
}

// agentProcess is `orb chat` with args, run from environ (the container's)
// plus rendered (the agent file's platform settings), serving its ACP socket
// for turns started from outside.
func agentProcess(args, environ []string, rendered map[string]string, image layout) process {
	agent := process{argv: append([]string{"orb", "chat"}, args...), dir: image.workspace, env: []string{"ORB_SECRETS_FD=3", "ORB_AUTH_FD=4", "ORB_ACP_SOCKET=" + image.socket}}
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
	agent.secrets = secrets.String()
	return agent
}
