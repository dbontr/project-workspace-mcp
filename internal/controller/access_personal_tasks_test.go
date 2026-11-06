package controller

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestCloudflareAccessPersonalTaskLibrary(t *testing.T) {
	db, server, httpServer := fixture(t)
	key, issuer := testOIDCIssuer(t, server)
	server.AccessVerifier = server.OIDC
	server.AccessVerifier.MaxTokenLifetime = 15 * 24 * time.Hour
	server.AccessOwnerEmail = "owner@example.com"
	server.OIDC = nil

	assertion := signedAccessAssertion(t, key, issuer, "project-workspace",
		"owner-subject", "owner@example.com", time.Now().Add(time.Hour).Unix())
	callMCP := func(name string, arguments map[string]any) map[string]any {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": arguments},
		})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, httpServer.URL+"/mcp/", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Cf-Access-Jwt-Assertion", assertion)
		res, err := httpServer.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var response map[string]any
		if err := json.NewDecoder(res.Body).Decode(&response); err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusOK {
			t.Fatalf("MCP %s: status %d, response %v", name, res.StatusCode, response)
		}
		result, ok := response["result"].(map[string]any)
		if !ok || asBool(result["isError"]) {
			t.Fatalf("MCP %s denied: %v", name, response)
		}
		return result
	}

	empty := callMCP("task_list", map[string]any{})
	emptyData, ok := empty["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("missing task list result: %v", empty)
	}
	if tasks, ok := emptyData["result"].([]any); !ok || len(tasks) != 0 {
		t.Fatalf("new Access owner must have an empty task library: %v", emptyData)
	}

	created := callMCP("task_create", map[string]any{
		"title": "Circuit simulation", "tags": []string{"circuits"},
	})
	task, ok := created["structuredContent"].(map[string]any)
	if !ok || asString(task["id"]) == "" {
		t.Fatalf("missing personal task ID: %v", created)
	}
	id := asString(task["id"])
	if _, err := db.PersonalTaskInfo("controller-owner", id); err == nil {
		t.Fatal("Access-owned task was readable by a different principal")
	}

	callMCP("task_save_text", map[string]any{
		"task_id": id, "name": "notes.txt",
		"body": "Verified Access personal task", "expected_revision": 0,
	})
	listed := callMCP("task_list", map[string]any{})
	data := listed["structuredContent"].(map[string]any)
	tasks, ok := data["result"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("Access-owned task not listed: %v", data)
	}
	first, ok := tasks[0].(map[string]any)
	if !ok || asString(first["id"]) != id {
		t.Fatalf("unexpected task list entry: %v", tasks)
	}
}

func TestCloudflareAccessTaskOwnerValidation(t *testing.T) {
	allowed := "cloudflare-access:https://example.cloudflareaccess.com#owner@example.com"
	if !validTaskOwner(allowed) {
		t.Fatal("verified Access identity must be eligible for personal task storage")
	}
	for _, owner := range []string{
		"", "cloudflare-access:", "cloudflare-access:http://example.com#owner@example.com",
		"cloudflare-access:https://example.com#",
		"cloudflare-access:https://example.com#owner@example.com\n",
	} {
		if validTaskOwner(owner) {
			t.Fatalf("invalid personal task owner accepted: %q", owner)
		}
	}
}
