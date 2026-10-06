// Package multicall lets a package answer when Orb's binary runs under another
// name, through a link such as `ln -s orb /usr/local/bin/<name>`.
package multicall

import "io"

// Command runs Orb started as a registered name, with its arguments.
type Command func(args []string, stdin io.Reader, stdout, stderr io.Writer) int

var commands = map[string]Command{}

// Register makes Orb run command when started as name.
func Register(name string, command Command) { commands[name] = command }

// Lookup returns the command registered for name, or nil.
func Lookup(name string) Command { return commands[name] }
