//go:build darwin

package cli

import "os/exec"

func openBrowser(target string) error { return exec.Command("open", target).Start() }
