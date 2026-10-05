// Command oge checks coding-agent work instead of letting the agent grade its
// own homework.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("oge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "oge %s\n", resolvedVersion())
		return 0
	}
	fmt.Fprintln(stderr, "oge: nothing to do yet; only --version is implemented")
	return 2
}

func resolvedVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}
