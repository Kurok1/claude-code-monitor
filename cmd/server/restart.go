package main

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"syscall"
	"time"
)

const (
	gracefulStopTimeout = 5 * time.Second
	forcedStopTimeout   = 2 * time.Second
	pollInterval        = 100 * time.Millisecond
)

// stopExistingInstance locates the process listening on addr, sends SIGTERM,
// and waits for the process to exit. Escalates to SIGKILL if the process
// does not exit within gracefulStopTimeout. Lets a fresh `./bin/server`
// invocation transparently take over from a stale one — the default for
// dev workflows.
//
// Readiness is gated on the PID actually disappearing (kill(pid, 0) → ESRCH)
// rather than on the listener port becoming free. The OTLP server closes its
// gRPC socket at the very start of GracefulStop(), but the rest of the
// shutdown chain (writer.Stop → db.Close) still needs to run before the
// DuckDB file lock is released. Waiting on the port would return too early
// and the new instance would fail to acquire the DB lock.
func stopExistingInstance(addr string, logger *slog.Logger) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("split host:port: %w", err)
	}

	pids, err := findListenerPIDs(port)
	if err != nil {
		return fmt.Errorf("locate listener: %w", err)
	}
	if len(pids) == 0 {
		return errors.New("port occupied but listener pid not resolved")
	}

	logger.Info("another instance is listening; restarting", "addr", addr, "pids", pids)
	signalAll(pids, syscall.SIGTERM, logger)

	if waitForPIDsExit(pids, gracefulStopTimeout) {
		logger.Info("previous instance exited gracefully")
		return nil
	}

	logger.Warn("previous instance did not exit after SIGTERM; escalating", "pids", pids)
	signalAll(pids, syscall.SIGKILL, logger)
	if waitForPIDsExit(pids, forcedStopTimeout) {
		logger.Info("previous instance killed")
		return nil
	}
	return fmt.Errorf("pids %v still alive after SIGKILL", pids)
}

func signalAll(pids []int, sig syscall.Signal, logger *slog.Logger) {
	for _, pid := range pids {
		proc, err := os.FindProcess(pid)
		if err != nil {
			logger.Warn("find process", "pid", pid, "err", err)
			continue
		}
		if err := proc.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
			logger.Warn("signal process", "pid", pid, "sig", sig.String(), "err", err)
		}
	}
}

// waitForPIDsExit polls each PID with signal 0 (the standard "is this
// process alive?" probe) until every PID returns ESRCH or the timeout
// elapses. Returns true only when all listed processes have exited —
// which on Unix is exactly when any file locks they held are released.
func waitForPIDsExit(pids []int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if allExited(pids) {
			return true
		}
		time.Sleep(pollInterval)
	}
	return allExited(pids)
}

func allExited(pids []int) bool {
	for _, pid := range pids {
		if pidAlive(pid) {
			return false
		}
	}
	return true
}

// pidAlive returns true while pid is still a live process. Uses signal 0,
// which checks delivery permission without actually sending anything: nil
// means the process exists, ESRCH means it's gone, EPERM means it exists
// but we can't signal it (still alive for our purposes).
func pidAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}
	if errors.Is(err, syscall.EPERM) {
		return true
	}
	return false
}
