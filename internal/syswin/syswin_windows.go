//go:build windows

package syswin

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const createNoWindow = 0x08000000

// HideWindowAttr returns SysProcAttr that prevents console windows from flashing on Windows.
func HideWindowAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}

// RevealInExplorer opens Windows Explorer highlighting the target file, or opening the folder if target is a directory.
func RevealInExplorer(absPath string) error {
	clean := filepath.Clean(absPath)
	info, err := os.Stat(clean)
	if err == nil && info.IsDir() {
		cmd := exec.Command("explorer.exe", clean)
		return cmd.Start()
	}
	cmd := exec.Command("explorer.exe", "/select,"+clean)
	return cmd.Start()
}

// OpenAppWindow launches the local web UI in a Chromium/Edge app window, falling back to the default browser.
func OpenAppWindow(targetURL string) error {
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("LocalAppData"), "Google", "Chrome", "Application", "chrome.exe"),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
		filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
	}
	for _, bin := range candidates {
		if bin == "" {
			continue
		}
		if _, err := os.Stat(bin); err == nil {
			cmd := exec.Command(bin, "--app="+targetURL, "--window-size=1260,840")
			if err := cmd.Start(); err == nil {
				return nil
			}
		}
	}
	cmd := exec.Command("rundll32", "url.dll,FileProtocolHandler", targetURL)
	return cmd.Start()
}

const folderDialogScript = `
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.Application]::EnableVisualStyles()
$f = New-Object System.Windows.Forms.Form
$f.TopMost = $true
$d = New-Object System.Windows.Forms.FolderBrowserDialog
$d.UseDescriptionForTitle = $true
if ($env:IPADOWN_DIALOG_TITLE) { $d.Description = $env:IPADOWN_DIALOG_TITLE }
if ($env:IPADOWN_DIALOG_INITIAL -and (Test-Path $env:IPADOWN_DIALOG_INITIAL)) { $d.SelectedPath = $env:IPADOWN_DIALOG_INITIAL }
if ($d.ShowDialog($f) -eq [System.Windows.Forms.DialogResult]::OK) {
    [Console]::Out.Write($d.SelectedPath)
}
$f.Dispose()
`

const ipaDialogScript = `
[Console]::OutputEncoding = [System.Text.Encoding]::UTF8
Add-Type -AssemblyName System.Windows.Forms
[System.Windows.Forms.Application]::EnableVisualStyles()
$f = New-Object System.Windows.Forms.Form
$f.TopMost = $true
$d = New-Object System.Windows.Forms.OpenFileDialog
$d.Filter = "IPA Files (*.ipa)|*.ipa|All Files (*.*)|*.*"
if ($env:IPADOWN_DIALOG_TITLE) { $d.Title = $env:IPADOWN_DIALOG_TITLE }
if ($env:IPADOWN_DIALOG_INITIAL -and (Test-Path $env:IPADOWN_DIALOG_INITIAL)) { $d.InitialDirectory = $env:IPADOWN_DIALOG_INITIAL }
if ($d.ShowDialog($f) -eq [System.Windows.Forms.DialogResult]::OK) {
    [Console]::Out.Write($d.FileName)
}
$f.Dispose()
`

// PickFolder displays a native Windows folder selection dialog. Empty string means cancelled.
func PickFolder(title, initialDir string) (string, error) {
	return runFixedDialog(folderDialogScript, title, initialDir)
}

// PickIPAFile displays a native Windows file selection dialog filtered for .ipa files. Empty string means cancelled.
func PickIPAFile(title, initialDir string) (string, error) {
	return runFixedDialog(ipaDialogScript, title, initialDir)
}

func runFixedDialog(script, title, initialDir string) (string, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-STA", "-Command", script)
	cmd.SysProcAttr = HideWindowAttr()
	cmd.Env = append(os.Environ(),
		"IPADOWN_DIALOG_TITLE="+title,
		"IPADOWN_DIALOG_INITIAL="+initialDir,
	)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("dialog error: %w (%s)", err, strings.TrimSpace(errBuf.String()))
	}
	return strings.TrimSpace(out.String()), nil
}
