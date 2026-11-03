package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dbontr/project-workspace-mcp/internal/worker"
)

// The two controller instances use different TLS listeners and independently
// open the same durable database; no worker administrator token is issued.
func TestTLSPairingSurvivesControllerRestartAndRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	db, err := OpenDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewServer(db, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	tlsFirst := httptest.NewTLSServer(first)
	code, created := call(t, tlsFirst, http.MethodPost, "/api/projects",
		testOwner, map[string]any{"id": "demo", "title": "Demo"})
	wantOK(t, code, created)
	code, pairing := call(t, tlsFirst, http.MethodPost, "/api/pairing", testOwner, nil)
	wantOK(t, code, pairing)
	capabilities := map[string]any{"cpu": 4, "memory_mb": 4096, "max_jobs": 2,
		"docker": true, "devices": []any{}}
	client := &worker.Client{Base: tlsFirst.URL, Node: "worker-one", HTTP: tlsFirst.Client()}
	token, err := client.Pair(context.Background(), asString(pairing["pairing_code"]), capabilities)
	if err != nil || len(token) < 20 {
		t.Fatalf("TLS worker enrollment failed: %v", err)
	}
	code, _ = call(t, tlsFirst, http.MethodPost, "/api/pair", "", map[string]any{
		"name": "worker-two", "pairing_code": pairing["pairing_code"], "capabilities": capabilities,
	})
	if code == http.StatusOK {
		t.Fatal("replayed single-use pairing code was accepted")
	}
	code, _ = call(t, tlsFirst, http.MethodGet, "/api/status", token, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("worker credential gained administrator access: HTTP %d", code)
	}
	heartbeat := map[string]any{"capabilities": capabilities}
	code, accepted := call(t, tlsFirst, http.MethodPost,
		"/api/worker/worker-one/heartbeat", token, heartbeat)
	wantOK(t, code, accepted)

	tlsFirst.Close()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenDatabase(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	second, err := NewServer(reopened, testOwner)
	if err != nil {
		t.Fatal(err)
	}
	tlsSecond := httptest.NewTLSServer(second)
	defer tlsSecond.Close()
	code, _ = call(t, tlsSecond, http.MethodPost,
		"/api/worker/worker-one/heartbeat", token, heartbeat)
	if code != http.StatusOK {
		t.Fatalf("restarted TLS controller lost the worker credential: HTTP %d", code)
	}
	code, _ = call(t, tlsSecond, http.MethodPost,
		"/api/nodes/worker-one/revoke", token, nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("worker credential could revoke its own identity: HTTP %d", code)
	}
	code, revoked := call(t, tlsSecond, http.MethodPost,
		"/api/nodes/worker-one/revoke", testOwner, nil)
	wantOK(t, code, revoked)
	if revoked["revoked"] != true || revoked["worker_id"] != "worker-one" {
		t.Fatalf("administrator revocation did not confirm the worker: %v", revoked)
	}
	code, _ = call(t, tlsSecond, http.MethodPost,
		"/api/worker/worker-one/heartbeat", token, heartbeat)
	if code != http.StatusUnauthorized {
		t.Fatalf("revoked worker credential still accepted: HTTP %d", code)
	}
	code, nextPairing := call(t, tlsSecond, http.MethodPost, "/api/pairing", testOwner, nil)
	wantOK(t, code, nextPairing)
	code, _ = call(t, tlsSecond, http.MethodPost, "/api/pair", "", map[string]any{
		"name": "worker-one", "pairing_code": nextPairing["pairing_code"], "capabilities": capabilities,
	})
	if code == http.StatusOK {
		t.Fatal("revoked worker silently regained access under its existing name")
	}
}
