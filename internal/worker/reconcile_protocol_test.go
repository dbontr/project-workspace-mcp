package worker

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestWorkerReconnectReportsVerifiedCleanupBeforeNewClaims(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "cleanup-invoked")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"ps\" ]; then touch " + shellQuote(marker) + "; fi\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var reconciled atomic.Bool
	var orderFailure atomic.Bool
	var reconcileCount atomic.Int32
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/worker/worker-one/heartbeat":
			_, _ = w.Write([]byte(`{"ok":true}`))
		case "/api/worker/worker-one/reconcile":
			if _, err := os.Stat(marker); err != nil {
				orderFailure.Store(true)
			}
			reconcileCount.Add(1)
			reconciled.Store(true)
			_, _ = w.Write([]byte(`{"expired_jobs":1,"node":"worker-one"}`))
		case "/api/worker/worker-one/operation/claim":
			if !reconciled.Load() {
				orderFailure.Store(true)
			}
			_, _ = w.Write([]byte(`{"operation":null}`))
		case "/api/worker/worker-one/claim":
			if !reconciled.Load() {
				orderFailure.Store(true)
			}
			_, _ = w.Write([]byte(`{"job":null}`))
			cancel()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	runner := NewRunner(nil)
	runner.ReconcileOnConnect = true
	client := &Client{Base: server.URL, Node: "worker-one", Token: strings.Repeat("k", 32), HTTP: server.Client()}
	err := client.runWithHeartbeatInterval(ctx, runner, 50*time.Millisecond)
	if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected worker shutdown: %v", err)
	}
	if !reconciled.Load() || reconcileCount.Load() != 1 || orderFailure.Load() {
		t.Fatalf("worker did not reconcile once before claiming: reconciled=%t requests=%d orderingFailure=%t",
			reconciled.Load(), reconcileCount.Load(), orderFailure.Load())
	}
}
