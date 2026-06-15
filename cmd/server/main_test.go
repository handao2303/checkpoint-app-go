package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunStartupTasksRunsConcurrently(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	done := make(chan error, 1)

	task := func(name string) startupTask {
		return startupTask{
			name: name,
			run: func() error {
				started <- name
				<-release
				return nil
			},
		}
	}

	go func() {
		done <- runStartupTasks(task("postgres"), task("supabase jwks"))
	}()

	waitForSignal(t, started)
	waitForSignal(t, started)
	close(release)

	if err := <-done; err != nil {
		t.Fatalf("runStartupTasks() error = %v", err)
	}
}

func TestRunStartupTasksWaitsForTasksAndReturnsNamedError(t *testing.T) {
	wantErr := errors.New("connection failed")
	finished := make(chan struct{})

	err := runStartupTasks(
		startupTask{
			name: "postgres",
			run:  func() error { return wantErr },
		},
		startupTask{
			name: "supabase jwks",
			run: func() error {
				close(finished)
				return nil
			},
		},
	)

	if !errors.Is(err, wantErr) {
		t.Fatalf("runStartupTasks() error = %v, want %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "postgres") {
		t.Fatalf("runStartupTasks() error = %q, want dependency name", err)
	}
	select {
	case <-finished:
	default:
		t.Fatal("runStartupTasks() returned before all tasks finished")
	}
}

func waitForSignal(t *testing.T, signals <-chan string) {
	t.Helper()
	select {
	case <-signals:
	case <-time.After(time.Second):
		t.Fatal("startup tasks did not run concurrently")
	}
}
