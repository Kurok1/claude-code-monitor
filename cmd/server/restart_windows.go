//go:build windows

package main

import "errors"

// findListenerPIDs is not implemented on Windows: lsof is unavailable and
// SIGTERM cannot be delivered via os.Process.Signal. Stop the previous
// instance manually (taskkill /PID <pid> /F) and re-run, or use
// `-skip-if-running` to fall back to the idempotent no-op behavior.
func findListenerPIDs(port string) ([]int, error) {
	return nil, errors.New("restart-on-conflict is not supported on windows; stop the existing process manually or use -skip-if-running")
}
