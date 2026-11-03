package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

type toolDefinition struct {
	Name, Description string
	Required          []string
	Optional          []string
}

var tools = []toolDefinition{
	{"list_projects", "List registered projects", nil, nil},
	{"create_project", "Create a project", []string{"project_id", "title"}, nil},
	{"read_project_note", "Read a versioned note", []string{"project_id", "name"}, nil},
	{"write_project_note", "Write a note with a revision guard", []string{"project_id", "name", "body", "expected_version"}, nil},
	{"search_project_knowledge", "Search project notes", []string{"project_id", "query"}, nil},
	{"list_compute_nodes", "List workers and availability", nil, nil},
	{"submit_compute_job", "Queue a one-shot command", []string{"project_id", "command"}, []string{"image", "cpu", "memory_mb"}},
	{"list_project_jobs", "List compute jobs", []string{"project_id"}, nil},
	{"link_github_repository", "Link a repository to a project", []string{"project_id", "repository"}, []string{"role", "default_ref"}},
	{"list_github_repositories", "List linked repositories", []string{"project_id"}, nil},
	{"workspace_create", "Reserve resources and start a workspace", []string{"project_id"}, []string{"image", "cpu", "memory_mb", "duration_minutes", "environment", "hardware", "usb_passthrough", "usb_node", "usb_device_id"}},
	{"workspace_list", "List project workspaces", []string{"project_id"}, nil},
	{"workspace_call", "Queue a command or file operation", []string{"project_id", "workspace_id", "action", "payload"}, nil},
	{"workspace_operation", "Get a worker-confirmed operation result", []string{"project_id", "workspace_id", "operation_id"}, nil},
	{"workspace_import_artifact", "Import a stored artifact into a workspace", []string{"project_id", "workspace_id", "artifact_id", "path"}, nil},
	{"workspace_destroy", "Stop a workspace and release confirmed resources", []string{"project_id", "workspace_id"}, nil},
	{"list_hardware", "List hardware and leases", nil, nil},
	{"environment_define", "Save a versioned OCI environment definition", []string{"project_id", "name", "base_image"}, []string{"dockerfile", "expected_version", "context", "architecture"}},
	{"environment_list", "List environment definitions", []string{"project_id"}, nil},
	{"environment_build", "Build an OCI environment", []string{"project_id", "name", "workspace_id"}, nil},
	{"environment_export", "Save an OCI image archive", []string{"project_id", "name", "workspace_id"}, nil},
	{"environment_import", "Load an OCI image archive", []string{"project_id", "name", "workspace_id", "artifact_id"}, nil},
	{"workspace_snapshot", "Save workspace filesystem contents", []string{"project_id", "workspace_id"}, nil},
	{"workspace_snapshot_list", "List workspace snapshots", []string{"project_id", "workspace_id"}, nil},
	{"workspace_restore", "Restore a workspace snapshot", []string{"project_id", "workspace_id", "snapshot_id"}, nil},
	{"github_repository_api", "Call a repository-scoped GitHub endpoint", []string{"project_id", "repository", "method", "endpoint"}, []string{"params", "payload"}},
	{"github_repository", "Read repository metadata", []string{"project_id", "repository"}, nil},
	{"github_read_file", "Read a file at a GitHub revision", []string{"project_id", "repository", "path", "ref"}, nil},
	{"github_list_issues", "List issues", []string{"project_id", "repository"}, []string{"state"}},
	{"github_list_pull_requests", "List pull requests", []string{"project_id", "repository"}, []string{"state"}},
	{"github_create_issue", "Create a GitHub issue", []string{"project_id", "repository", "title"}, []string{"body"}},
	{"github_checkout_repository", "Checkout a GitHub archive into a workspace", []string{"project_id", "workspace_id", "repository", "ref"}, nil},
	{"github_publish_artifacts", "Publish project artifacts to a revision-guarded draft PR", []string{"project_id", "repository", "base_branch", "expected_head", "changes", "title"}, []string{"body"}},
	{"list_project_artifacts", "List durable project artifacts", []string{"project_id"}, nil},
	{"github_project_attach", "Bind a GitHub Projects V2 board by URL or node ID", []string{"project_id"}, []string{"github_project_id", "url"}},
	{"github_project_board", "Read the linked GitHub Projects board reference", []string{"project_id"}, nil},
	{"github_project_items", "Read live GitHub Project items and fields", []string{"project_id"}, []string{"after"}},
	{"github_project_fields", "List GitHub Project fields and select-option IDs", []string{"project_id"}, []string{"after"}},
	{"github_project_add_draft", "Add a draft planning item to the GitHub Project", []string{"project_id", "title"}, []string{"body"}},
	{"github_project_add_item", "Add a linked repository issue or PR to the GitHub Project", []string{"project_id", "repository", "kind", "number"}, nil},
	{"github_project_set_status", "Update a GitHub Project single-select field", []string{"project_id", "item_id", "field_id", "option_id"}, nil},
	{"repository_memberships", "List all project repositories and their roles", []string{"project_id"}, nil},
	{"library_create", "Register an independent shared research or Lean library", []string{"library_id", "title", "kind"}, []string{"description"}},
	{"library_list", "List shared libraries independent of projects", nil, nil},
	{"library_add_source", "Attach a GitHub repository at an immutable commit to a library", []string{"library_id", "repository", "ref"}, []string{"path", "kind"}},
	{"library_sources", "List pinned GitHub sources for a shared library", []string{"library_id"}, nil},
	{"library_link", "Reference a shared library from a project without copying it", []string{"project_id", "library_id"}, nil},
	{"library_resolve", "Resolve linked library sources and content hashes", []string{"project_id", "library_id"}, nil},
	{"library_read_file", "Read a file from an authorized pinned shared library source", []string{"project_id", "library_id", "repository", "file"}, []string{"source_path"}},
	{"library_import_object", "Import a shared large object into an isolated workspace", []string{"project_id", "library_id", "workspace_id", "name", "path"}, nil},
	{"library_checkout_source", "Checkout a pinned shared library source into a workspace", []string{"project_id", "library_id", "workspace_id", "repository"}, []string{"source_path"}},
}

func kindFor(key string) string {
	switch key {
	case "cpu":
		return "number"
	case "memory_mb", "duration_minutes", "expected_version", "number":
		return "integer"
	case "usb_passthrough":
		return "boolean"
	case "hardware", "payload", "params", "changes":
		return "object"
	}
	return "string"
}
func (t toolDefinition) schema() map[string]any {
	properties := map[string]any{}
	for _, key := range append(append([]string{}, t.Required...), t.Optional...) {
		properties[key] = map[string]any{"type": kindFor(key)}
	}
	required := t.Required
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": properties, "required": required, "additionalProperties": false}
}
func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "MCP transport supports POST requests")
		return
	}
	if !strings.Contains(r.Header.Get("Accept"), "application/json") && !strings.Contains(r.Header.Get("Accept"), "*/*") {
		fail(w, 406, "MCP requires JSON accept header")
		return
	}
	var msg struct {
		Version string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  map[string]any  `json:"params"`
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if e := dec.Decode(&msg); e != nil {
		fail(w, 400, "invalid MCP JSON-RPC request")
		return
	}
	if msg.Version != "2.0" {
		fail(w, 400, "unsupported JSON-RPC version")
		return
	}
	if len(msg.ID) == 0 {
		w.WriteHeader(202)
		return
	}
	response := map[string]any{"jsonrpc": "2.0", "id": json.RawMessage(msg.ID)}
	switch msg.Method {
	case "initialize":
		response["result"] = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": "project-workspace-mcp", "version": "0.6.0"}, "instructions": "Operations are asynchronous. Wait for worker-confirmed completion before claiming success."}
	case "ping":
		response["result"] = map[string]any{}
	case "tools/list":
		list := []map[string]any{}
		for _, def := range tools {
			list = append(list, map[string]any{"name": def.Name, "description": def.Description, "inputSchema": def.schema()})
		}
		response["result"] = map[string]any{"tools": list}
	case "tools/call":
		name := asString(msg.Params["name"])
		args, _ := msg.Params["arguments"].(map[string]any)
		result, e := s.callTool(name, args)
		if e != nil {
			response["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": e.Error()}}, "isError": true}
		} else {
			out := map[string]any{"result": result}
			if object, ok := result.(map[string]any); ok {
				out = object
			}
			response["result"] = map[string]any{"content": []map[string]any{{"type": "text", "text": encode(result)}}, "structuredContent": out, "isError": false}
		}
	default:
		response["error"] = map[string]any{"code": -32601, "message": "MCP method not found"}
	}
	reply(w, 200, response)
}
func (s *Server) callTool(name string, a map[string]any) (any, error) {
	var spec *toolDefinition
	for i := range tools {
		if tools[i].Name == name {
			spec = &tools[i]
			break
		}
	}
	if spec == nil {
		return nil, errors.New("unknown MCP tool")
	}
	if a == nil {
		a = map[string]any{}
	}
	for _, key := range spec.Required {
		if _, ok := a[key]; !ok {
			return nil, fmt.Errorf("missing required argument %s", key)
		}
	}
	get := func(k string) string { return asString(a[k]) }
	project := get("project_id")
	workspace := get("workspace_id")
	db := s.DB
	switch name {
	case "list_projects":
		return db.Projects()
	case "create_project":
		return db.CreateProject(project, get("title"))
	case "read_project_note":
		return db.ReadNote(project, get("name"))
	case "write_project_note":
		return db.WriteNote(project, get("name"), get("body"), asInt(a["expected_version"]))
	case "search_project_knowledge":
		return db.SearchNotes(project, get("query"))
	case "list_compute_nodes":
		return db.Nodes()
	case "submit_compute_job":
		image := get("image")
		if image == "" {
			image = "debian:bookworm-slim"
		}
		cpu := asFloat(a["cpu"])
		if cpu == 0 {
			cpu = 1
		}
		mem := asInt(a["memory_mb"])
		if mem == 0 {
			mem = 512
		}
		return db.SubmitJob(project, get("command"), image, cpu, mem)
	case "list_project_jobs":
		return db.Jobs(project)
	case "link_github_repository":
		return db.LinkRepositoryWithRole(project, get("repository"), get("role"), get("default_ref"))
	case "list_github_repositories":
		return db.Repositories(project)
	case "workspace_create":
		var req WorkspaceRequest
		if e := JSONRequest(a, &req); e != nil {
			return nil, e
		}
		return db.CreateWorkspace(req)
	case "workspace_list":
		return db.Workspaces(project)
	case "workspace_call":
		payload, _ := a["payload"].(map[string]any)
		return db.CallWorkspace(project, workspace, get("action"), payload)
	case "workspace_operation":
		return db.Operation(project, workspace, get("operation_id"))
	case "workspace_import_artifact":
		return db.QueueImportFile(project, workspace, get("artifact_id"), get("path"))
	case "workspace_destroy":
		return db.StopWorkspace(project, workspace)
	case "list_hardware":
		return db.Hardware()
	case "environment_define":
		return db.EnvironmentDefine(project, get("name"), get("base_image"), get("dockerfile"), get("context"), get("architecture"), asInt(a["expected_version"]))
	case "environment_list":
		return db.Environments(project)
	case "environment_build":
		return db.EnvironmentAction(project, get("name"), workspace, "build_image", "")
	case "environment_export":
		return db.EnvironmentAction(project, get("name"), workspace, "export_image", "")
	case "environment_import":
		return db.EnvironmentAction(project, get("name"), workspace, "load_image", get("artifact_id"))
	case "workspace_snapshot":
		return db.CallWorkspace(project, workspace, "snapshot", map[string]any{})
	case "workspace_snapshot_list":
		return db.SnapshotList(project, workspace)
	case "workspace_restore":
		return db.RestoreSnapshot(project, workspace, get("snapshot_id"))
	case "github_project_attach":
		if (get("github_project_id") == "") == (get("url") == "") {
			return nil, errors.New("specify one GitHub Project ID or URL")
		}
		var metadata map[string]any
		var e error
		if get("url") != "" {
			metadata, e = s.GitHub.ProjectByURL(get("url"))
		} else {
			metadata, e = s.GitHub.ProjectMetadata(get("github_project_id"))
		}
		if e != nil {
			return nil, e
		}
		return db.AttachPlanningBoard(project, metadata)
	case "github_project_board":
		return db.PlanningBoard(project)
	case "github_project_items":
		board, e := db.PlanningBoard(project)
		if e != nil {
			return nil, e
		}
		return s.GitHub.ProjectItems(asString(board["github_id"]), get("after"))
	case "github_project_fields":
		board, e := db.PlanningBoard(project)
		if e != nil {
			return nil, e
		}
		return s.GitHub.ProjectFields(asString(board["github_id"]), get("after"))
	case "github_project_add_draft":
		board, e := db.PlanningBoard(project)
		if e != nil {
			return nil, e
		}
		return s.GitHub.ProjectAddDraft(asString(board["github_id"]), get("title"), get("body"))
	case "github_project_add_item":
		if !db.RepositoryLinked(project, get("repository")) {
			return nil, errors.New("repository not linked to project")
		}
		board, e := db.PlanningBoard(project)
		if e != nil {
			return nil, e
		}
		return s.GitHub.ProjectAddIssue(asString(board["github_id"]), get("repository"), get("kind"), asInt(a["number"]))
	case "github_project_set_status":
		board, e := db.PlanningBoard(project)
		if e != nil {
			return nil, e
		}
		return s.GitHub.ProjectSetSingleSelect(asString(board["github_id"]), get("item_id"), get("field_id"), get("option_id"))
	case "repository_memberships":
		return db.RepositoryMemberships(project)
	case "library_create":
		return db.CreateLibrary(get("library_id"), get("title"), get("description"), get("kind"))
	case "library_list":
		return db.Libraries()
	case "library_add_source":
		if _, e := db.Library(get("library_id")); e != nil {
			return nil, e
		}
		revision, e := s.GitHub.Commit(get("repository"), get("ref"))
		if e != nil {
			return nil, e
		}
		return db.AddLibrarySource(get("library_id"), get("repository"), revision, get("path"), get("kind"))
	case "library_sources":
		return db.LibrarySources(get("library_id"))
	case "library_link":
		return db.LinkLibrary(project, get("library_id"))
	case "library_resolve":
		return db.ResolveLibrary(project, get("library_id"))
	case "library_read_file":
		if !db.libraryAttached(project, get("library_id")) {
			return nil, errors.New("library not linked to project")
		}
		source, e := db.LibrarySource(get("library_id"), get("repository"), get("source_path"))
		if e != nil {
			return nil, e
		}
		if !libraryFileAllowed(get("file"), get("source_path")) {
			return nil, errors.New("file outside pinned library source")
		}
		return s.GitHub.File(get("repository"), get("file"), asString(source["revision"]))
	case "library_import_object":
		return db.QueueLibraryObjectImport(project, workspace, get("library_id"), get("name"), get("path"))
	case "library_checkout_source":
		return s.CheckoutLibrarySource(project, workspace, get("library_id"), get("repository"), get("source_path"))
	case "list_project_artifacts":
		return db.Artifacts(project)
	}
	repository := get("repository")
	if !db.RepositoryLinked(project, repository) {
		return nil, errors.New("repository is not linked to this project")
	}
	g := s.GitHub
	switch name {
	case "github_repository_api":
		params, _ := a["params"].(map[string]any)
		return g.RepositoryAPI(repository, get("method"), get("endpoint"), params, a["payload"])
	case "github_repository":
		return g.Repo(repository)
	case "github_read_file":
		revision, e := g.Commit(repository, get("ref"))
		if e != nil {
			return nil, e
		}
		return g.File(repository, get("path"), revision)
	case "github_list_issues", "github_list_pull_requests":
		state := get("state")
		if state == "" {
			state = "open"
		}
		return g.Issues(repository, state, name == "github_list_pull_requests")
	case "github_create_issue":
		return g.IssueCreate(repository, get("title"), get("body"))
	case "github_checkout_repository":
		return s.CheckoutArchive(project, workspace, repository, get("ref"))
	case "github_publish_artifacts":
		base := get("base_branch")
		expected := get("expected_head")
		changes, ok := a["changes"].(map[string]any)
		if !ok {
			return nil, errors.New("changes must map GitHub file paths to artifact IDs")
		}
		files := map[string][]byte{}
		for path, id := range changes {
			meta, e := db.ArtifactInfo(project, asString(id))
			if e != nil {
				return nil, e
			}
			onDisk, e := db.VerifyArtifact(meta)
			if e != nil {
				return nil, e
			}
			data, e := readBounded(onDisk, 1<<20)
			if e != nil {
				return nil, e
			}
			files[path] = data
		}
		return g.Publish(repository, base, expected, files, get("title"), get("body"))
	}
	return nil, errors.New("unsupported MCP tool")
}
func readBounded(path string, limit int64) ([]byte, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	data, e := io.ReadAll(io.LimitReader(f, limit+1))
	if e != nil {
		return nil, e
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return data, nil
}
