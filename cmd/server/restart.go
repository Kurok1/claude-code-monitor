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
// and waits for the port to free. Escalates to SIGKILL if the process does
// not exit within gracefulStopTimeout. Lets a fresh `./bin/server` invocation
// transparently take over from a stale one — the default for dev workflows.
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

	if waitForPortFree(addr, gracefulStopTimeout) {
		logger.Info("previous instance exited gracefully")
		return nil
	}

	logger.Warn("previous instance did not exit after SIGTERM; escalating", "pids", pids)
	signalAll(pids, syscall.SIGKILL, logger)
	if waitForPortFree(addr, forcedStopTimeout) {
		logger.Info("previous instance killed")
		return nil
	}
	return fmt.Errorf("port %s still occupied after SIGKILL", addr)
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

func waitForPortFree(addr string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !alreadyListening(addr) {
			return true
		}
		time.Sleep(pollInterval)
	}
	return false
}
