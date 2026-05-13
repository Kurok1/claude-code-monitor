//go:build !windows

package main

import (
	"net"
	"os"
	"slices"
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

func TestWaitForPortFreeReturnsTrueWhenClosed(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()

	done := make(chan bool, 1)
	go func() {
		done <- waitForPortFree(addr, 2*time.Second)
	}()

	time.Sleep(150 * time.Millisecond)
	_ = ln.Close()

	select {
	case ok := <-done:
		if !ok {
			t.Fatalf("waitForPortFree returned false after close")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("waitForPortFree did not return in time")
	}
}

func TestWaitForPortFreeTimesOut(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	if waitForPortFree(ln.Addr().String(), 300*time.Millisecond) {
		t.Fatal("waitForPortFree returned true while port is still held")
	}
}
