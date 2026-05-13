//go:build !windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// findListenerPIDs reports the PIDs of processes currently bound to a TCP
// listener on port. Uses lsof, which ships on macOS and most Linux distros.
// Returns nil when no process matches; an error only when lsof itself fails.
func findListenerPIDs(port string) ([]int, error) {
	cmd := exec.Command("lsof", "-nP", "-iTCP:"+port, "-sTCP:LISTEN", "-t")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, fmt.Errorf("lsof: %w", err)
	}

	self := os.Getpid()
	seen := make(map[int]struct{})
	var pids []int
	for _, field := range strings.Fields(string(out)) {
		n, err := strconv.Atoi(field)
		if err != nil {
			continue
		}
		if n == self {
			continue
		}
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		pids = append(pids, n)
	}
	return pids, nil
}
