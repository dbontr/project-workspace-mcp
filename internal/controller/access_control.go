package controller

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type caller struct {
	Principal string
	Root      bool
}

func (s *Server) authenticate(r *http.Request) (caller, error) {
	if s.isAuthorized(r) {
		return caller{Principal: "controller-owner", Root: true}, nil
	}
	if assertion := r.Header.Get("Cf-Access-Jwt-Assertion"); assertion != "" {
		if s.AccessVerifier == nil {
			return caller{}, errors.New("Cloudflare Access identity is not configured")
		}
		email, err := s.AccessVerifier.VerifiedEmail(assertion)
		if err != nil || email != s.AccessOwnerEmail || s.DB.TokenRevoked(hashToken(assertion)) {
			return caller{}, errors.New("invalid Cloudflare Access identity")
		}
		return caller{Principal: "cloudflare-access:" + s.AccessVerifier.Issuer + "#" + email, Root: true}, nil
	}
	header := r.Header.Get("Authorization")
	if s.OIDC == nil || !strings.HasPrefix(header, "Bearer ") {
		return caller{}, errors.New("invalid controller credential")
	}
	token := strings.TrimPrefix(header, "Bearer ")
	principal, err := s.OIDC.Verify(token)
	if err != nil || s.DB.TokenRevoked(hashToken(token)) {
		return caller{}, errors.New("invalid or revoked OIDC credential")
	}
	return caller{Principal: principal}, nil
}

func (s *Server) authorizedFor(project string, who caller, minimum int) bool {
	if who.Root {
		return true
	}
	role, err := s.DB.ProjectRole(project, who.Principal)
	return err == nil && grantLevel(role) >= minimum
}

var readableTools = map[string]bool{
	"list_projects":    true,
	"pool_list_images": true, "pool_list_software": true, "pool_list_programs": true,
	"read_project_note": true, "search_project_knowledge": true,
	"research_sources": true, "research_search": true, "research_read_passage": true, "research_topics": true, "research_browse_topic": true,
	"study_pack_prepare": true,
	"knowledge_context":  true, "knowledge_contributions": true, "knowledge_contribution": true, "knowledge_search_contributions": true,
	"knowledge_policy":    true,
	"knowledge_git_read":  true,
	"git_repository_head": true, "git_read_file": true,
	"list_project_jobs": true, "list_github_repositories": true,
	"task_claim_list": true, "task_claim_status": true, "resource_plan": true,
	"workspace_list": true, "workspace_operation": true,
	"environment_list": true, "workspace_snapshot_list": true, "workspace_checkpoint_stop_status": true,
	"list_project_artifacts": true, "artifact_info": true, "artifact_read_base64": true, "artifact_view_image": true,
	"github_repository": true, "github_read_file": true,
	"github_list_issues": true, "github_list_pull_requests": true,
	"github_get_issue": true, "github_list_branches": true,
	"github_get_pull_request": true, "github_verify_pull_request": true,
	"github_project_board": true, "github_project_items": true, "github_project_fields": true,
	"repository_memberships": true, "library_resolve": true,
	"library_read_file": true,
}
var rootTools = map[string]bool{
	"create_project": true, "list_compute_nodes": true, "worker_revoke": true, "list_hardware": true,
	"task_worker_authorize": true, "task_worker_access": true, "task_hardware_recover": true,
	"pool_register_image": true, "pool_register_software": true,
	"pool_propose_program": true, "pool_review_program": true, "pool_update_entry": true, "pool_entry_info": true, "pool_events": true,
	"library_create": true, "library_list": true, "library_sources": true, "library_add_source": true,
	"research_register_source": true, "research_ingest_passages": true,
	"library_attachment_send": true,
	"link_github_repository":  true, "github_repository_api": true, "github_pat_readiness": true,
	"github_account_search": true, "github_account_repositories": true, "github_account_repository": true,
	"github_account_rename": true, "github_account_transfer": true, "github_account_reconcile": true,
	"github_account_settings": true, "github_account_configure": true, "github_account_archive": true,
	"git_link_repository":        true,
	"external_resource_register": true, "external_resource_list": true, "external_resource_leases": true,
	"external_resource_reserve": true, "external_resource_release": true,
	"knowledge_configure": true, "knowledge_git_publish": true,
	"github_create_issue": true, "github_update_issue": true,
	"github_comment_issue": true, "github_create_branch": true,
	"github_set_branch_auto_delete": true, "github_cleanup_merged_branch": true,
	"github_create_pull_request": true, "github_update_pull_request": true,
	"github_mark_pull_request_ready": true, "github_merge_pull_request": true,
	"github_publish_artifacts": true, "github_project_attach": true,
	"github_project_add_draft": true, "github_project_add_item": true,
	"github_project_set_status": true,
}

func (s *Server) authorizeTool(who caller, name string, args map[string]any) bool {
	if who.Root {
		if !readableTools[name] {
			s.DB.AuditAccess(who.Principal, asString(args["project_id"]), "mcp:"+name, true)
		}
		return true
	}
	if rootTools[name] {
		s.DB.AuditAccess(who.Principal, asString(args["project_id"]), "mcp:"+name, false)
		return false
	}
	if name == "upload_begin" || name == "upload_chunk" || name == "upload_status" ||
		name == "upload_finish" || name == "upload_cancel" {
		if project := asString(args["project_id"]); project != "" {
			allowed := s.authorizedFor(project, who, 2)
			s.DB.AuditAccess(who.Principal, project, "mcp:"+name, allowed)
			return allowed
		}
		// Personal task checks occur inside each DB operation and bind the
		// session to the exact authenticated principal.
		return asString(args["task_id"]) != ""
	}
	if name == "task_list" || name == "task_create" || name == "task_details" ||
		name == "task_update" || name == "task_files" || name == "task_file_info" ||
		name == "task_compute_submit" || name == "task_compute_jobs" ||
		name == "task_save_text" || name == "task_send_base64" || name == "task_read_base64" ||
		name == "task_attach_base64" || name == "task_study_pack_import" ||
		name == "personal_task_claim_list" || name == "personal_task_claim_status" ||
		name == "personal_task_claim" || name == "personal_task_claim_renew" ||
		name == "personal_task_claim_finish" {
		return true
	}
	if name == "task_export_project" {
		allowed := s.authorizedFor(asString(args["project_id"]), who, 2)
		s.DB.AuditAccess(who.Principal, asString(args["project_id"]), "mcp:"+name, allowed)
		return allowed
	}
	if name == "git_provider_capabilities" || name == "list_projects" || name == "pool_list_images" || name == "pool_list_software" || name == "pool_list_programs" {
		return true
	}
	project := asString(args["project_id"])
	needed := 2
	if readableTools[name] {
		needed = 1
	}
	if name == "workspace_create" && (asBool(args["usb_passthrough"]) || asFloat(decodeAnyObject(args["hardware"])["usb"]) > 0) {
		needed = 3
	}
	if name == "library_link" || name == "workspace_prune_volume" {
		needed = 3
	}
	ok := s.authorizedFor(project, who, needed)
	if !ok || !readableTools[name] {
		s.DB.AuditAccess(who.Principal, project, "mcp:"+name, ok)
	}
	return ok
}

func decodeAnyObject(value any) map[string]any {
	object, _ := value.(map[string]any)
	if object == nil {
		return map[string]any{}
	}
	return object
}

func (s *Server) authorizeREST(r *http.Request, p []string, who caller) bool {
	if who.Root {
		if r.Method != http.MethodGet {
			s.DB.AuditAccess(who.Principal, projectFromPath(p), "rest:"+r.Method+":"+r.URL.Path, true)
		}
		return true
	}
	if len(p) >= 2 && p[1] == "tasks" {
		// Only identity-bound handlers can read or mutate personal tasks.
		if r.Method != http.MethodGet {
			s.DB.AuditAccess(who.Principal, "", "rest:"+r.Method+":"+r.URL.Path, true)
		}
		return true
	}
	if len(p) == 2 && p[1] == "projects" && r.Method == http.MethodGet {
		return true
	}
	if len(p) == 2 && p[1] == "whoami" && r.Method == http.MethodGet {
		return true
	}
	if len(p) >= 3 && p[1] == "projects" {
		project := p[2]
		needed := 1
		if r.Method != http.MethodGet {
			needed = 2
		}
		if len(p) == 4 && p[3] == "resource-plan" && r.Method == http.MethodPost {
			needed = 1
		}
		if len(p) >= 4 && (p[3] == "github" || p[3] == "planning" || p[3] == "libraries") && r.Method != http.MethodGet {
			s.DB.AuditAccess(who.Principal, project, "rest:"+r.Method+":"+r.URL.Path, false)
			return false
		}
		if len(p) >= 4 && p[3] == "permissions" ||
			len(p) == 7 && p[3] == "workspaces" && p[5] == "volume" && p[6] == "prune" {
			needed = 3
		}
		ok := s.authorizedFor(project, who, needed)
		if !ok || r.Method != http.MethodGet {
			s.DB.AuditAccess(who.Principal, project, "rest:"+r.Method+":"+r.URL.Path, ok)
		}
		return ok
	}
	if len(p) == 2 && p[1] == "workspaces" && r.Method == http.MethodPost {
		if r.Body == nil {
			return false
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		r.Body.Close()
		if err != nil || len(data) >= 64<<10 {
			return false
		}
		r.Body = io.NopCloser(bytes.NewReader(data))
		var request WorkspaceRequest
		if json.Unmarshal(data, &request) != nil {
			return false
		}
		needed := 2
		if request.USBPassthrough || asFloat(decodeAnyObject(request.Hardware)["usb"]) > 0 {
			needed = 3
		}
		ok := s.authorizedFor(request.ProjectID, who, needed)
		s.DB.AuditAccess(who.Principal, request.ProjectID, "rest:POST:/api/workspaces", ok)
		return ok
	}
	s.DB.AuditAccess(who.Principal, "", "rest:"+r.Method+":"+r.URL.Path, false)
	return false
}

func projectFromPath(p []string) string {
	if len(p) >= 3 && p[1] == "projects" {
		return p[2]
	}
	return ""
}

func (s *Server) handleAccess(w http.ResponseWriter, r *http.Request, p []string, who caller) bool {
	if len(p) == 2 && p[1] == "whoami" && r.Method == http.MethodGet {
		response := map[string]any{"principal": who.Principal, "administrator": who.Root}
		if !who.Root {
			response["token_sha256"] = hashToken(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		}
		reply(w, 200, response)
		return true
	}
	if len(p) == 2 && p[1] == "projects" && r.Method == http.MethodGet && !who.Root {
		rows, err := s.DB.ProjectsForPrincipal(who.Principal)
		send(w, rows, err)
		return true
	}

	if len(p) == 2 && p[1] == "revocations" {
		if !who.Root {
			fail(w, 403, "administrator permission required")
			return true
		}
		switch r.Method {
		case http.MethodGet:
			rows, err := s.DB.TokenRevocations()
			send(w, rows, err)
		case http.MethodPost:
			var in struct {
				SHA256  string `json:"sha256"`
				Expires int64  `json:"expires"`
			}
			if err := bodyJSON(r, &in); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			if err := s.DB.RevokeToken(in.SHA256, in.Expires); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			reply(w, 200, map[string]any{"revoked": true, "sha256": in.SHA256})
		default:
			fail(w, 405, "method not allowed")
		}
		return true
	}
	if len(p) == 2 && p[1] == "access-audit" && r.Method == http.MethodGet {
		if !who.Root {
			fail(w, 403, "administrator permission required")
			return true
		}
		limit := 100
		if text := r.URL.Query().Get("limit"); text != "" {
			v, err := strconv.Atoi(text)
			if err != nil {
				fail(w, 400, "invalid audit limit")
				return true
			}
			limit = v
		}
		rows, err := s.DB.AccessAudit(limit)
		send(w, rows, err)
		return true
	}
	if len(p) == 4 && p[1] == "projects" && p[3] == "permissions" {
		if !s.authorizedFor(p[2], who, 3) {
			fail(w, 403, "project owner permission required")
			return true
		}
		switch r.Method {
		case http.MethodGet:
			rows, err := s.DB.ProjectGrants(p[2])
			send(w, rows, err)
		case http.MethodPost:
			var in struct {
				Principal    string `json:"principal"`
				Role         string `json:"role"`
				ExpectedRole string `json:"expected_role"`
			}
			if err := bodyJSON(r, &in); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			row, err := s.DB.SetProjectGrant(p[2], in.Principal, in.Role, in.ExpectedRole)
			send(w, row, err)
		default:
			fail(w, 405, "method not allowed")
		}
		return true
	}
	return false
}
