package cli

import (
	"errors"
	"flag"
	"fmt"
	"runtime/debug"
	"strings"
)

// Version is set to the release tag at build time using -ldflags -X.
var Version = "dev"

func (a *App) version(args []string) (int, error) {
	set := flag.NewFlagSet("version", flag.ContinueOnError)
	set.SetOutput(new(strings.Builder))
	if err := set.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(a.Stdout, "Usage: mockingo version")
			return 0, nil
		}
		return 2, fmt.Errorf("invalid arguments: %w", err)
	}
	if set.NArg() != 0 {
		return 2, fmt.Errorf("invalid arguments: version does not accept positional arguments")
	}
	version := Version
	if version == "dev" {
		// Go can embed a module version from VCS or go install module@version.
		if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
			version = info.Main.Version
		}
	}
	fmt.Fprintln(a.Stdout, version)
	return 0, nil
}
