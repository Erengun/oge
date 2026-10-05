// Command oge checks coding-agent work instead of letting the agent grade its
// own homework.
package main

import (
	"io"
	"os"
	"runtime/debug"

	"github.com/erengun/oge/internal/cli"
)

// version is stamped at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	env := cli.ProcessEnv(resolvedVersion())
	env.Stdout, env.Stderr = stdout, stderr
	return cli.Main(env, args)
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
