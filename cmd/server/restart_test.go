//go:build !windows

package main

import (
	"net"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestFindListenerPIDsFiltersSelf(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split: %v", err)
	}

	pids, err := findListenerPIDs(port)
	if err != nil {
		t.Fatalf("findListenerPIDs: %v", err)
	}

	// We are the only listener; the function must filter our own pid out
	// so the caller never accidentally signals itself.
	if slices.Contains(pids, os.Getpid()) {
		t.Fatalf("findListenerPIDs returned own pid: %v", pids)
	}
}

// TestWaitForPIDsExitReturnsTrueWhenChildExits spawns a short-lived child,
// kills it, and verifies the wait returns true. Reaping happens via the
// Process.Wait() call in the goroutine so we don't leave a zombie that
// would keep pidAlive returning true.
func TestWaitForPIDsExitReturnsTrueWhenChildExits(t *testing.T) {
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	pid := cmd.Process.Pid

	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
	}()

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal child: %v", err)
	}

	if !waitForPIDsExit([]int{pid}, 3*time.Second) {
		t.Fatalf("waitForPIDsExit returned false; pid %d still alive", pid)
	}

	select {
	case <-reaped:
	case <-time.After(time.Second):
		t.Fatal("child not reaped after waitForPIDsExit reported success")
	}
}

// TestWaitForPIDsExitTimesOut uses a pid we control and never kill — the
// wait must exhaust its timeout and return false.
func TestWaitForPIDsExitTimesOut(t *testing.T) {
	cmd := exec.Command("sleep", "5")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})

	if waitForPIDsExit([]int{cmd.Process.Pid}, 200*time.Millisecond) {
		t.Fatal("waitForPIDsExit returned true while child is still alive")
	}
}

// TestPidAliveReportsDeadPID picks an obviously-free PID (the largest int32 +
// our own pid shifted) and confirms pidAlive returns false. Picking a high
// value avoids accidentally probing a real process.
func TestPidAliveReportsDeadPID(t *testing.T) {
	// /proc/sys/kernel/pid_max defaults to 4194304 on Linux; macOS caps lower
	// but values past 1<<29 are still safely unused. Convert via strconv to
	// keep linters happy.
	deadPID, _ := strconv.Atoi("536870911") // 1<<29 - 1
	if pidAlive(deadPID) {
		t.Fatalf("pidAlive(%d) = true, want false", deadPID)
	}
}
