//go:build linux

package cli

import "os/exec"

func openBrowser(target string) error { return exec.Command("xdg-open", target).Start() }
