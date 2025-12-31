// Package e2e provides command execution utilities for E2E tests.
//
// Feature: zstack-ovn-kubernetes-cni
// Validates: Requirements 14.1, 14.4
package e2e

import (
	"os/exec"
)

// Command wraps exec.Cmd for easier testing.
type Command struct {
	*exec.Cmd
}

// NewCommand creates a new command.
//
// Parameters:
//   - name: command name
//   - args: command arguments
//
// Returns:
//   - *Command: the command wrapper
func NewCommand(name string, args ...string) *Command {
	return &Command{
		Cmd: exec.Command(name, args...),
	}
}

// RunCommand runs a command and returns the output.
//
// Parameters:
//   - name: command name
//   - args: command arguments
//
// Returns:
//   - string: command output
//   - error: execution error if any
func RunCommand(name string, args ...string) (string, error) {
	cmd := NewCommand(name, args...)
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// CommandExists checks if a command exists in PATH.
//
// Parameters:
//   - name: command name
//
// Returns:
//   - bool: true if command exists
func CommandExists(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
