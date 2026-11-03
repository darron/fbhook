package main

import (
	"context"
	"fmt"
	"github.com/darron/fbhook/internal/cli"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
)

var buildRevision = ""
var buildState = "unknown"

func main() {
	if info, ok := debug.ReadBuildInfo(); ok && buildRevision == "" {
		for _, v := range info.Settings {
			switch v.Key {
			case "vcs.revision":
				buildRevision = v.Value
			case "vcs.modified":
				if v.Value == "true" {
					buildState = "dirty"
				} else {
					buildState = "clean"
				}
			}
		}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e := cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, cli.BuildInfo{Revision: buildRevision, State: buildState}); e != nil {
		_, _ = fmt.Fprintln(os.Stderr, "fbhook:", e)
		os.Exit(1)
	}
}
