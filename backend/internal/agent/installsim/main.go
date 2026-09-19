// Command installsim drives the agent's real installer entry points against
// the local machine. It lives inside the agent package so the cross-distro
// test rig (proot sandboxes) can exercise the exact same code paths an
// enrolled agent would — InstallRuntime / RemoveRuntime /
// InstallDatabaseEngine — plus the static-build path directly for
// benchmarking. Output is machine-parsable ("RESULT <what> OK|FAIL").
//
// Usage:
//
//	installsim install php 8.3 node 22 ...
//	installsim remove php 8.3 ...
//	installsim engine mariadb postgresql
//	installsim force-static php 8.2   (bypasses the distro short-circuit)
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/epicbyte/epicpanel/backend/internal/agent"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: installsim install|remove|engine|force-static <args...>")
		os.Exit(2)
	}
	mode := os.Args[1]
	e := agent.NewExecutor()
	ctx := context.Background()

	switch mode {
	case "engine":
		rc := 0
		for _, eng := range os.Args[2:] {
			if err := e.InstallDatabaseEngine(ctx, eng); err != nil {
				fmt.Printf("RESULT engine %s FAIL: %s\n", eng, strings.ReplaceAll(err.Error(), "\n", " | "))
				rc = 1
			} else {
				fmt.Printf("RESULT engine %s OK\n", eng)
			}
		}
		os.Exit(rc)
	}

	rc := 0
	args := os.Args[2:]
	for i := 0; i+1 < len(args); i += 2 {
		typ, ver := args[i], args[i+1]
		var err error
		if mode == "remove" {
			err = e.RemoveRuntime(ctx, "", typ, ver)
		} else {
			err = e.InstallRuntime(ctx, "", typ, ver, func(pct int, stage string) {
				fmt.Printf("[%s %s] %3d%% %s\n", typ, ver, pct, stage)
			})
		}
		if err != nil {
			fmt.Printf("RESULT %s %s FAIL: %s\n", typ, ver, strings.ReplaceAll(err.Error(), "\n", " | "))
			rc = 1
		} else {
			fmt.Printf("RESULT %s %s OK\n", typ, ver)
		}
	}
	os.Exit(rc)
}
