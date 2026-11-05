package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestScheduledOffsiteBackupStartsBeforeFirstInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := make(chan string, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runScheduledOffsiteBackups(ctx, time.Hour, func(context.Context) (string, error) {
			calls <- "baseline"
			return "verified-snapshot", nil
		}, func(snapshot string, err error) {
			if err == nil && snapshot == "verified-snapshot" {
				cancel()
			}
		})
	}()
	select {
	case got := <-calls:
		if got != "baseline" {
			t.Fatalf("unexpected backup call: %s", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backup did not start before the first scheduled interval")
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("backup scheduler did not stop on cancellation")
	}
	select {
	case <-calls:
		t.Fatal("backup ran after cancellation")
	default:
	}
}

func TestScheduledOffsiteBackupRetriesFailures(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	attempts := 0
	var reports []bool
	go func() {
		defer close(done)
		runScheduledOffsiteBackups(ctx, 10*time.Millisecond,
			func(context.Context) (string, error) {
				attempts++
				if attempts == 1 {
					return "", errors.New("repository temporarily unavailable")
				}
				return "verified-snapshot", nil
			},
			func(snapshot string, err error) {
				reports = append(reports, err == nil && snapshot == "verified-snapshot")
				if err == nil {
					cancel()
				}
			})
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("failed backup did not retry and complete")
	}
	if attempts != 2 || len(reports) != 2 || reports[0] || !reports[1] {
		t.Fatalf("unexpected retry sequence: attempts=%d reports=%v", attempts, reports)
	}
}

func TestScheduledOffsiteBackupHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	runScheduledOffsiteBackups(ctx, time.Hour,
		func(context.Context) (string, error) {
			called = true
			return "", nil
		}, func(string, error) {})
	if called {
		t.Fatal("canceled scheduler initiated an offsite backup")
	}
}
