//go:build windows

package cli

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"

	"golang.org/x/sys/windows"
)

func openBrowser(target string) error {
	return openBrowserWithShellExecute(target, windows.ShellExecute)
}

func openBrowserWithShellExecute(target string, execute func(windows.Handle, *uint16, *uint16, *uint16, *uint16, int32) error) error {
	file, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return fmt.Errorf("invalid browser URL: %w", err)
	}
	verb, _ := windows.UTF16PtrFromString("open")

	// Shell handlers may use COM. Initialization and cleanup must happen on
	// the same OS thread, including when COM was already initialized (S_FALSE).
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err = windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
	if err != nil && !errors.Is(err, syscall.Errno(windows.S_FALSE)) {
		return fmt.Errorf("initialize browser launcher: %w", err)
	}
	defer windows.CoUninitialize()

	// Pass the complete URL directly to the default handler. OAuth query
	// parameters must survive unchanged; avoid rundll32/url.dll and command-line
	// interpretation of the nested, percent-encoded redirect_uri.
	if err := execute(0, verb, file, nil, nil, windows.SW_SHOWNORMAL); err != nil {
		return fmt.Errorf("open default browser: %w", err)
	}
	return nil
}
