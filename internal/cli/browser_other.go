//go:build !windows && !darwin && !linux

package cli

import "errors"

func openBrowser(string) error {
	return errors.New("automatic browser opening is unsupported on this platform")
}
