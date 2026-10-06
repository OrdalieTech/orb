// Command orb-agent is the team agent image's entrypoint: it sets an agent up
// from one file, /agent/agent.yaml, and runs it, `orb chat` as the agent's user
// and each sidecar its platforms declare as another. What it knows of a
// platform is its row in the chat/platforms catalog.
//
//	orb-agent [orb chat arguments]   run the agent (as root, the image's entrypoint)
//	orb-agent check <file>           validate an agent file and print its plan
package main

import (
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
)

func main() {
	if len(os.Args) == 3 && os.Args[1] == "check" {
		plan, err := load(os.Args[2], image)
		if err != nil {
			fail(err)
		}
		fmt.Printf("platforms: %s\n", strings.Join(plan.platforms, ", "))
		for _, path := range slices.Sorted(maps.Keys(plan.files)) {
			if plan.files[path] != nil {
				fmt.Printf("writes %s\n", path)
			}
		}
		for _, name := range slices.Sorted(maps.Keys(plan.env)) {
			fmt.Printf("sets %s\n", name)
		}
		return
	}
	code, err := run(os.Args[1:], image)
	if err != nil {
		fail(err)
	}
	os.Exit(code)
}

func fail(err error) {
	_, _ = fmt.Fprintln(os.Stderr, "orb-agent: "+err.Error())
	os.Exit(1)
}
