package controller

import (
	"net/http"
	"strconv"
)

// handleGitHubWorkflow serves explicit, repository-scoped mutations through the
// same owner-authentication boundary as the MCP interface.
func (s *Server) handleGitHubWorkflow(w http.ResponseWriter, r *http.Request, parts []string) bool {
	if len(parts) < 5 || parts[1] != "projects" || parts[3] != "github" {
		return false
	}
	project := parts[2]
	repo := r.URL.Query().Get("repository")
	if !s.DB.RepositoryLinked(project, repo) {
		fail(w, 403, "repository is not linked to this project")
		return true
	}
	gh := s.GitHub
	sendGitHub := func(value any, err error) { send(w, value, err) }
	method, category := r.Method, parts[4]
	number := 0
	if len(parts) >= 6 && category != "branches" {
		parsed, err := strconv.Atoi(parts[5])
		if err != nil || !validNumber(parsed) {
			fail(w, 400, "invalid item number")
			return true
		}
		number = parsed
	}
	switch category {
	case "issues":
		if len(parts) == 5 {
			switch method {
			case "GET":
				state := r.URL.Query().Get("state")
				if state == "" {
					state = "open"
				}
				sendGitHub(gh.Issues(repo, state, false))
				return true
			case "POST":
				var request struct {
					Title string `json:"title"`
					Body  string `json:"body"`
				}
				if err := bodyJSON(r, &request); err != nil {
					fail(w, 400, err.Error())
					return true
				}
				sendGitHub(gh.IssueCreate(repo, request.Title, request.Body))
				return true
			}
		}
		if len(parts) == 6 {
			switch method {
			case "GET":
				sendGitHub(gh.Issue(repo, number))
				return true
			case "PATCH":
				var request struct {
					ExpectedState string  `json:"expected_state"`
					State         string  `json:"state"`
					StateReason   string  `json:"state_reason"`
					Title         *string `json:"title"`
					Body          *string `json:"body"`
				}
				if err := bodyJSON(r, &request); err != nil {
					fail(w, 400, err.Error())
					return true
				}
				sendGitHub(gh.IssueUpdate(repo, number, request.ExpectedState, request.State, request.StateReason, request.Title, request.Body))
				return true
			}
		}
		if len(parts) == 7 && parts[6] == "comments" && method == "POST" {
			var request struct{ Body string }
			if err := bodyJSON(r, &request); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			sendGitHub(gh.IssueComment(repo, number, request.Body))
			return true
		}
	case "branches":
		if len(parts) == 6 && parts[5] == "auto-delete" && method == "POST" {
			var request struct {
				Enabled bool `json:"enabled"`
				Confirm bool `json:"confirm"`
			}
			if err := bodyJSON(r, &request); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			sendGitHub(gh.SetAutoDeleteMergedBranches(repo, request.Enabled, request.Confirm))
			return true
		}
		if len(parts) == 5 {
			if method == "GET" {
				sendGitHub(gh.Branches(repo))
				return true
			}
			if method == "POST" {
				var request struct {
					Branch  string `json:"branch"`
					FromRef string `json:"from_ref"`
				}
				if err := bodyJSON(r, &request); err != nil {
					fail(w, 400, err.Error())
					return true
				}
				sendGitHub(gh.BranchCreate(repo, request.Branch, request.FromRef))
				return true
			}
		}
	case "pulls":
		if len(parts) == 5 {
			if method == "GET" {
				state := r.URL.Query().Get("state")
				if state == "" {
					state = "open"
				}
				sendGitHub(gh.Issues(repo, state, true))
				return true
			}
			if method == "POST" {
				var request struct {
					BaseBranch string `json:"base_branch"`
					HeadBranch string `json:"head_branch"`
					Title      string `json:"title"`
					Body       string `json:"body"`
					Draft      bool   `json:"draft"`
				}
				if err := bodyJSON(r, &request); err != nil {
					fail(w, 400, err.Error())
					return true
				}
				sendGitHub(gh.PullCreate(repo, request.BaseBranch, request.HeadBranch, request.Title, request.Body, request.Draft))
				return true
			}
		}
		if len(parts) == 6 {
			if method == "GET" {
				sendGitHub(gh.Pull(repo, number))
				return true
			}
			if method == "PATCH" {
				var request struct {
					ExpectedHead string  `json:"expected_head"`
					Title        *string `json:"title"`
					Body         *string `json:"body"`
				}
				if err := bodyJSON(r, &request); err != nil {
					fail(w, 400, err.Error())
					return true
				}
				sendGitHub(gh.PullUpdate(repo, number, request.ExpectedHead, request.Title, request.Body))
				return true
			}
		}
		if len(parts) == 7 {
			if method == "GET" && parts[6] == "verify" {
				sendGitHub(gh.PullVerification(repo, number))
				return true
			}
			if method == "POST" {
				var request struct {
					ExpectedHead  string `json:"expected_head"`
					MergeMethod   string `json:"merge_method"`
					Confirm       bool   `json:"confirm"`
					CleanupBranch *bool  `json:"cleanup_branch"`
				}
				if err := bodyJSON(r, &request); err != nil {
					fail(w, 400, err.Error())
					return true
				}
				switch parts[6] {
				case "ready":
					sendGitHub(gh.PullReady(repo, number, request.ExpectedHead))
					return true
				case "merge":
					sendGitHub(gh.PullMerge(repo, number, request.ExpectedHead, request.MergeMethod, request.Confirm, request.CleanupBranch == nil || *request.CleanupBranch))
					return true
				case "cleanup":
					sendGitHub(gh.CleanupMergedBranch(repo, number, request.ExpectedHead, request.Confirm))
					return true
				}
			}
		}
	}
	fail(w, 405, "unsupported GitHub workflow method")
	return true
}
