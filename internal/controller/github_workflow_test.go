package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const workflowHead = "1111111111111111111111111111111111111111"
const workflowMerge = "2222222222222222222222222222222222222222"

type providerState struct {
	merged           bool
	deleted          bool
	protected        bool
	moved            bool
	failCheck        bool
	missingChecks    bool
	openReference    bool
	fork             bool
	draft            bool
	mergeCount       int
	deleteCount      int
	issueClosed      bool
	issueUpdateCount int
	autoDelete       bool
	autoDeleteWrites int
}

func fakeGitHubProvider(t *testing.T, state *providerState) (*GitHub, *httptest.Server) {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case r.Method == "GET" && path == "/repos/example/repo":
			fmt.Fprint(w, `{"full_name":"example/repo","default_branch":"main","id":123}`)
		case r.Method == "PATCH" && path == "/repos/example/repo":
			var input map[string]any
			_ = json.NewDecoder(r.Body).Decode(&input)
			state.autoDelete = input["delete_branch_on_merge"] == true
			state.autoDeleteWrites++
			_ = json.NewEncoder(w).Encode(map[string]any{"delete_branch_on_merge": state.autoDelete})
		case r.Method == "GET" && path == "/repos/example/repo/commits/main":
			fmt.Fprint(w, `{"sha":"`+workflowHead+`"}`)
		case r.Method == "POST" && path == "/repos/example/repo/git/refs":
			var input map[string]any
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input["ref"] != "refs/heads/pwm/research" || input["sha"] != workflowHead {
				t.Errorf("bad ref creation: %v", input)
			}
			fmt.Fprint(w, `{"ref":"refs/heads/pwm/research"}`)
		case r.Method == "GET" && path == "/repos/example/repo/branches":
			fmt.Fprint(w, `[{"name":"main","protected":true,"commit":{"sha":"`+workflowHead+`"}}]`)
		case r.Method == "GET" && (path == "/repos/example/repo/branches/pwm/research" || path == "/repos/example/repo/branches/feature/completed"):
			sha := workflowHead
			if state.moved {
				sha = workflowMerge
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"name": strings.TrimPrefix(path, "/repos/example/repo/branches/"), "protected": state.protected, "commit": map[string]any{"sha": sha}})
		case r.Method == "GET" && (path == "/repos/example/repo/git/ref/heads/pwm/research" || path == "/repos/example/repo/git/ref/heads/feature/completed"):
			sha := workflowHead
			if state.moved {
				sha = workflowMerge
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"object": map[string]any{"sha": sha}})
		case r.Method == "DELETE" && (path == "/repos/example/repo/git/refs/heads/pwm/research" || path == "/repos/example/repo/git/refs/heads/feature/completed"):
			state.deleted = true
			state.deleteCount++
			w.WriteHeader(204)
		case r.Method == "GET" && path == "/repos/example/repo/pulls/7":
			stateName := "open"
			if state.merged {
				stateName = "closed"
			}
			source := "example/repo"
			if state.fork {
				source = "other/fork"
			}
			branch := "pwm/research"
			if r.URL.Query().Get("unmanaged") == "true" {
				branch = "feature/completed"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"number": 7, "node_id": "PR_test_node_123456", "state": stateName, "merged": state.merged, "draft": state.draft,
				"mergeable": true, "mergeable_state": "clean", "html_url": "https://github.com/example/repo/pull/7",
				"head": map[string]any{"sha": workflowHead, "ref": branch, "repo": map[string]any{"full_name": source}},
				"base": map[string]any{"ref": "main", "sha": workflowMerge, "repo": map[string]any{"full_name": "example/repo"}},
			})
		case r.Method == "GET" && path == "/repos/example/repo/pulls":
			if state.openReference {
				fmt.Fprint(w, `[{"head":{"ref":"pwm/research","repo":{"full_name":"example/repo"}},"base":{"ref":"main","repo":{"full_name":"example/repo"}}}]`)
			} else {
				fmt.Fprint(w, `[]`)
			}
		case r.Method == "GET" && path == "/repos/example/repo/commits/"+workflowHead+"/check-runs":
			if state.missingChecks {
				fmt.Fprint(w, `{"total_count":0,"check_runs":[]}`)
				return
			}
			conclusion := "success"
			if state.failCheck {
				conclusion = "failure"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"total_count": 1, "check_runs": []map[string]any{{"name": "build", "status": "completed", "conclusion": conclusion}}})
		case r.Method == "GET" && path == "/repos/example/repo/commits/"+workflowHead+"/status":
			fmt.Fprint(w, `{"total_count":0,"state":"pending"}`)
		case r.Method == "PUT" && path == "/repos/example/repo/pulls/7/merge":
			var input map[string]any
			_ = json.NewDecoder(r.Body).Decode(&input)
			if input["sha"] != workflowHead {
				t.Errorf("merge not protected by head sha: %v", input)
			}
			state.merged = true
			state.mergeCount++
			fmt.Fprint(w, `{"merged":true,"sha":"`+workflowMerge+`"}`)
		case r.Method == "POST" && path == "/repos/example/repo/pulls":
			fmt.Fprint(w, `{"number":7,"html_url":"https://github.com/example/repo/pull/7","head":{"sha":"`+workflowHead+`"},"draft":false}`)
		case r.Method == "PATCH" && path == "/repos/example/repo/pulls/7":
			fmt.Fprint(w, `{"number":7,"title":"Edited"}`)
		case r.Method == "POST" && path == "/graphql":
			state.draft = false
			fmt.Fprint(w, `{"data":{"markPullRequestReadyForReview":{"pullRequest":{"id":"PR_test_node_123456","isDraft":false}}}}`)
		case r.Method == "GET" && path == "/repos/example/repo/issues/12":
			issueState := "open"
			if state.issueClosed {
				issueState = "closed"
			}
			fmt.Fprintf(w, `{"number":12,"title":"Track proof","state":%q}`, issueState)
		case r.Method == "PATCH" && path == "/repos/example/repo/issues/12":
			state.issueClosed = true
			state.issueUpdateCount++
			fmt.Fprint(w, `{"number":12,"state":"closed"}`)
		case r.Method == "POST" && path == "/repos/example/repo/issues/12/comments":
			fmt.Fprint(w, `{"id":34,"body":"Verified"}`)
		default:
			t.Errorf("unexpected GitHub request: %s %s", r.Method, path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	gh := NewGitHub("test-token")
	gh.API = upstream.URL
	return gh, upstream
}

func TestManagedGitHubLifecycle(t *testing.T) {
	state := &providerState{}
	gh, _ := fakeGitHubProvider(t, state)
	if _, err := gh.BranchCreate("example/repo", "feature/bypass", "main"); err == nil {
		t.Fatal("unmanaged branch created")
	}
	created, err := gh.BranchCreate("example/repo", "pwm/research", "main")
	if err != nil || gitObj(created)["from_sha"] != workflowHead {
		t.Fatalf("branch: %v %v", created, err)
	}
	pr, err := gh.PullCreate("example/repo", "main", "pwm/research", "Research", "Description", false)
	if err != nil || asInt(gitObj(pr)["number"]) != 7 {
		t.Fatalf("pull: %v %v", pr, err)
	}
	verified, err := gh.PullVerification("example/repo", 7)
	if err != nil || verified["ready_to_merge"] != true {
		t.Fatalf("verification: %v %v", verified, err)
	}
	if _, err = gh.PullMerge("example/repo", 7, workflowHead, "squash", false, true); err == nil {
		t.Fatal("merge without confirmation")
	}
	if _, err = gh.PullMerge("example/repo", 7, workflowMerge, "squash", true, true); err == nil {
		t.Fatal("merge with wrong head")
	}
	result, err := gh.PullMerge("example/repo", 7, workflowHead, "squash", true, true)
	if err != nil || result["merged"] != true || gitObj(result["cleanup"])["deleted"] != true {
		t.Fatalf("merge and cleanup: %v %v", result, err)
	}
	if state.mergeCount != 1 || state.deleteCount != 1 {
		t.Fatalf("unexpected provider mutations: %+v", state)
	}
}

func TestGitHubPullGatesRejectUnsafeOperations(t *testing.T) {
	for _, tc := range []struct {
		name           string
		change         func(*providerState)
		expectError    bool
		expectedReason string
	}{
		{"failing_checks", func(s *providerState) { s.failCheck = true }, true, ""},
		{"no_checks", func(s *providerState) { s.missingChecks = true }, true, ""},
		{"protected", func(s *providerState) { s.protected = true }, false, "protected"},
		{"moved_branch", func(s *providerState) { s.moved = true }, true, ""},
		{"referenced", func(s *providerState) { s.openReference = true }, false, "referenced"},
		{"fork", func(s *providerState) { s.fork = true }, false, "fork"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &providerState{merged: true}
			tc.change(state)
			gh, _ := fakeGitHubProvider(t, state)
			if tc.name == "failing_checks" || tc.name == "no_checks" {
				state.merged = false
				if _, err := gh.PullMerge("example/repo", 7, workflowHead, "squash", true, true); err == nil {
					t.Fatal("invalid checks allowed merge")
				}
			} else {
				result, err := gh.CleanupMergedBranch("example/repo", 7, workflowHead, true)
				if tc.expectError && err == nil {
					t.Fatalf("expected cleanup failure: %v", result)
				}
				if !tc.expectError && (err != nil || !strings.Contains(asString(result["reason"]), tc.expectedReason)) {
					t.Fatalf("unexpected cleanup: %v %v", result, err)
				}
			}
			if state.mergeCount != 0 || state.deleteCount != 0 {
				t.Fatalf("unsafe mutation: %+v", state)
			}
		})
	}
}

func TestGitHubIssueRevisionGuardAndComments(t *testing.T) {
	state := &providerState{}
	gh, _ := fakeGitHubProvider(t, state)
	if _, err := gh.IssueUpdate("example/repo", 12, "closed", "closed", "completed", nil, nil); err == nil {
		t.Fatal("stale issue state accepted")
	}
	if state.issueUpdateCount != 0 {
		t.Fatal("issue mutated on conflict")
	}
	result, err := gh.IssueUpdate("example/repo", 12, "open", "closed", "completed", nil, nil)
	if err != nil || gitObj(result)["state"] != "closed" {
		t.Fatalf("close: %v %v", result, err)
	}
	if _, err := gh.IssueComment("example/repo", 12, "Verified"); err != nil {
		t.Fatal(err)
	}
	if state.issueUpdateCount != 1 {
		t.Fatalf("unexpected issue updates: %d", state.issueUpdateCount)
	}
}

func TestGitHubWorkflowMCPAndRESTProjectScopes(t *testing.T) {
	db, s, srv := fixture(t)
	project(t, srv)
	if _, err := db.LinkRepositoryWithRole("demo", "example/repo", "source", "main"); err != nil {
		t.Fatal(err)
	}
	state := &providerState{}
	gh, _ := fakeGitHubProvider(t, state)
	s.GitHub = gh
	c, unlinked := call(t, srv, "POST", "/api/projects/demo/github/branches?repository=other/repo", testOwner, map[string]any{"branch": "pwm/research", "from_ref": "main"})
	if c != 403 {
		t.Fatalf("unlinked repository accepted: %d %v", c, unlinked)
	}
	c, result := call(t, srv, "POST", "/api/projects/demo/github/branches?repository=example/repo", testOwner, map[string]any{"branch": "pwm/research", "from_ref": "main"})
	wantOK(t, c, result)
	c, result = call(t, srv, "POST", "/mcp/", testOwner, map[string]any{"jsonrpc": "2.0", "id": 4, "method": "tools/call", "params": map[string]any{"name": "github_verify_pull_request", "arguments": map[string]any{"project_id": "demo", "repository": "example/repo", "number": 7}}})
	wantOK(t, c, result)
	if gitObj(gitObj(result["result"])["structuredContent"])["ready_to_merge"] != true {
		t.Fatalf("MCP verification failed: %v", result)
	}
	c, result = call(t, srv, "POST", "/api/projects/demo/github/pulls/7/merge?repository=example/repo", testOwner, map[string]any{"expected_head": workflowHead, "confirm": true, "merge_method": "squash"})
	wantOK(t, c, result)
	if result["merged"] != true || gitObj(result["cleanup"])["deleted"] != true {
		t.Fatalf("REST merge/cleanup failed: %v", result)
	}
}

func TestGitHubAutoDeleteRequiresExplicitRepositoryAuthorization(t *testing.T) {
	state := &providerState{}
	gh, _ := fakeGitHubProvider(t, state)
	if _, err := gh.SetAutoDeleteMergedBranches("example/repo", true, false); err == nil {
		t.Fatal("unconfirmed setting mutation accepted")
	}
	if state.autoDeleteWrites != 0 {
		t.Fatal("settings changed without confirmation")
	}
	setting, err := gh.SetAutoDeleteMergedBranches("example/repo", true, true)
	if err != nil || setting["delete_branch_on_merge"] != true || state.autoDeleteWrites != 1 {
		t.Fatalf("setting failed: %v %v", setting, err)
	}
}

func TestGitHubPullUpdateAndReadyForReview(t *testing.T) {
	state := &providerState{draft: true}
	gh, _ := fakeGitHubProvider(t, state)
	updated := "Evidence summary"
	if _, err := gh.PullUpdate("example/repo", 7, workflowMerge, &updated, nil); err == nil {
		t.Fatal("stale PR edit allowed")
	}
	if _, err := gh.PullUpdate("example/repo", 7, workflowHead, &updated, nil); err != nil {
		t.Fatal(err)
	}
	if result, err := gh.PullReady("example/repo", 7, workflowHead); err != nil || result == nil || state.draft {
		t.Fatalf("cannot ready PR: %v %v", result, err)
	}
}

func TestGitHubWorkflowRESTIssueAndAutoDelete(t *testing.T) {
	db, s, srv := fixture(t)
	project(t, srv)
	if _, err := db.LinkRepositoryWithRole("demo", "example/repo", "source", "main"); err != nil {
		t.Fatal(err)
	}
	state := &providerState{}
	gh, _ := fakeGitHubProvider(t, state)
	s.GitHub = gh
	issueURL := "/api/projects/demo/github/issues/12?repository=example/repo"
	c, result := call(t, srv, "PATCH", issueURL, testOwner, map[string]any{"expected_state": "closed", "state": "closed"})
	if c != 409 {
		t.Fatalf("stale issue state accepted: %d %v", c, result)
	}
	c, result = call(t, srv, "PATCH", issueURL, testOwner, map[string]any{"expected_state": "open", "state": "closed", "state_reason": "completed"})
	wantOK(t, c, result)
	if state.issueUpdateCount != 1 {
		t.Fatal("REST issue update was not applied")
	}
	settingURL := "/api/projects/demo/github/branches/auto-delete?repository=example/repo"
	c, _ = call(t, srv, "POST", settingURL, testOwner, map[string]any{"enabled": true, "confirm": false})
	if c == 200 {
		t.Fatal("unconfirmed auto delete setting accepted")
	}
	c, result = call(t, srv, "POST", settingURL, testOwner, map[string]any{"enabled": true, "confirm": true})
	wantOK(t, c, result)
	if result["delete_branch_on_merge"] != true {
		t.Fatalf("setting not confirmed: %v", result)
	}
}
