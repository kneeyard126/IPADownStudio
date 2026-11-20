//go:build !windows

package syswin

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
)

// HideWindowAttr returns nil on non-Windows platforms.
func HideWindowAttr() *syscall.SysProcAttr {
	return nil
}

// RevealInExplorer opens the system file manager on non-Windows hosts.
func RevealInExplorer(absPath string) error {
	clean := filepath.Clean(absPath)
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-R", clean).Start()
	}
	return exec.Command("xdg-open", filepath.Dir(clean)).Start()
}

// OpenAppWindow opens the browser on non-Windows hosts.
func OpenAppWindow(targetURL string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", targetURL).Start()
	}
	return exec.Command("xdg-open", targetURL).Start()
}

// PickFolder is unsupported outside Windows.
func PickFolder(title, initialDir string) (string, error) {
	return "", errors.New("native folder picker only supported on Windows")
}

// PickIPAFile is unsupported outside Windows.
func PickIPAFile(title, initialDir string) (string, error) {
	return "", errors.New("native file picker only supported on Windows")
}
