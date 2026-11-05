//go:build windows

// Command powershell.exe stands in for Windows PowerShell during the e2e run (scripts/e2e). It
// answers only setup's user-PATH registration, so the real registry is never changed, and runs
// every other command, including Copilot's hooks, with the real Windows PowerShell.
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	all := strings.Join(os.Args[1:], " ")
	if strings.Contains(all, "GetEnvironmentVariable('Path','User')") || strings.Contains(all, "SetEnvironmentVariable('Path'") {
		if f, err := os.OpenFile(os.Getenv("SHIM_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600); err == nil {
			f.WriteString("PATH registration answered by the stand-in\n")
			f.Close()
		}
		return
	}
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe"), os.Args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			os.Exit(exit.ExitCode())
		}
		os.Exit(1)
	}
}
