package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	selfupdate "mcpx/internal/update"
)

func runUpdate(args []string, build buildProvenance) int {
	flags := flag.NewFlagSet("update", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	checkOnly := flags.Bool("check", false, "check for a newer GitHub Release without installing it")
	targetVersion := flags.String("version", "", "install a specific release version, for example 0.5.0")
	flags.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: mcpx update [--check] [--version <version>]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "update: unexpected arguments: %s\n", strings.Join(flags.Args(), " "))
		flags.Usage()
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	result, err := selfupdate.Run(ctx, selfupdate.Options{
		CurrentVersion: build.Version,
		TargetVersion:  *targetVersion,
		CheckOnly:      *checkOnly,
		Progress: func(message string) {
			fmt.Fprintln(os.Stdout, message)
		},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "update: %v\n", err)
		return 1
	}
	if result.UpToDate {
		fmt.Printf("mcpx %s is already up to date.\n", result.TargetVersion)
		return 0
	}
	if result.CheckedOnly {
		fmt.Printf("Update available: %s → %s\n", result.CurrentVersion, result.TargetVersion)
		return 0
	}
	fmt.Printf("Updated mcpx %s → %s\n", result.CurrentVersion, result.TargetVersion)
	fmt.Printf("Installed %s\n", result.InstalledPath)
	return 0
}
