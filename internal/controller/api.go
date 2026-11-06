package controller

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

func (s *Server) handlePair(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		fail(w, 405, "method not allowed")
		return
	}
	var in struct {
		Code         string         `json:"pairing_code"`
		Name         string         `json:"name"`
		Capabilities map[string]any `json:"capabilities"`
	}
	if e := bodyJSON(r, &in); e != nil {
		fail(w, 400, "invalid enrollment request")
		return
	}
	result, e := s.DB.RedeemCode(in.Code, in.Name, in.Capabilities)
	send(w, result, e)
}
func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request, p []string) {
	db := s.DB
	method := r.Method
	if len(p) == 2 {
		switch p[1] {
		case "status":
			if method == "GET" {
				v, e := db.Status()
				send(w, v, e)
				return
			}
		case "projects":
			if method == "GET" {
				v, e := db.Projects()
				send(w, v, e)
				return
			}
			if method == "POST" {
				var in struct {
					ID    string `json:"id"`
					Title string `json:"title"`
				}
				if e := bodyJSON(r, &in); e != nil {
					fail(w, 400, e.Error())
					return
				}
				v, e := db.CreateProject(in.ID, in.Title)
				send(w, v, e)
				return
			}
		case "nodes":
			if method == "GET" {
				v, e := db.Nodes()
				send(w, v, e)
				return
			}
			if method == "POST" {
				var in struct {
					Name         string         `json:"name"`
					Capabilities map[string]any `json:"capabilities"`
				}
				if e := bodyJSON(r, &in); e != nil {
					fail(w, 400, e.Error())
					return
				}
				token, e := db.RegisterNode(in.Name, in.Capabilities)
				send(w, map[string]any{"node_id": in.Name, "worker_token": token}, e)
				return
			}
		case "hardware":
			if method == "GET" {
				v, e := db.Hardware()
				send(w, v, e)
				return
			}
		case "pairing":
			if method == "POST" {
				v, e := db.PairingCode()
				send(w, v, e)
				return
			}
		case "workspaces":
			if method == "POST" {
				var in WorkspaceRequest
				if e := bodyJSON(r, &in); e != nil {
					fail(w, 400, e.Error())
					return
				}
				v, e := db.CreateWorkspace(in)
				send(w, v, e)
				return
			}
		}
	}
	if len(p) == 4 && p[1] == "nodes" && p[3] == "revoke" && method == http.MethodPost {
		result, err := db.RevokeNode(p[2])
		send(w, result, err)
		return
	}
	if len(p) < 3 || p[1] != "projects" {
		fail(w, 404, "API route not found")
		return
	}
	project := p[2]
	if len(p) == 4 {
		switch p[3] {
		case "jobs":
			if method == "GET" {
				v, e := db.Jobs(project)
				send(w, v, e)
				return
			}
		case "repositories":
			if method == "GET" {
				v, e := db.Repositories(project)
				send(w, v, e)
				return
			}
		case "artifacts":
			if method == "GET" {
				v, e := db.Artifacts(project)
				send(w, v, e)
				return
			}
		case "workspaces":
			if method == "GET" {
				v, e := db.Workspaces(project)
				send(w, v, e)
				return
			}
		case "environments":
			if method == "GET" {
				v, e := db.Environments(project)
				send(w, v, e)
				return
			}
			if method == "POST" {
				var in struct {
					Name         string `json:"name"`
					Base         string `json:"base_image"`
					File         string `json:"dockerfile"`
					Expected     int    `json:"expected_version"`
					Context      string `json:"context"`
					Architecture string `json:"architecture"`
				}
				if e := bodyJSON(r, &in); e != nil {
					fail(w, 400, e.Error())
					return
				}
				v, e := db.EnvironmentDefine(project, in.Name, in.Base, in.File, in.Context, in.Architecture, in.Expected)
				send(w, v, e)
				return
			}
		case "search":
			if method == "GET" {
				v, e := db.SearchNotes(project, r.URL.Query().Get("q"))
				send(w, v, e)
				return
			}
		}
	}
	if len(p) == 5 {
		switch p[3] {
		case "notes":
			if method == "GET" {
				v, e := db.ReadNote(project, p[4])
				send(w, v, e)
				return
			}
			if method == "POST" {
				fail(w, http.StatusConflict, "local notes are read-only; use the configured Git knowledge remote")
				return
			}
		case "artifacts":
			if method == "GET" {
				meta, e := db.ArtifactInfo(project, p[4])
				s.serveArtifact(w, meta, e)
				return
			}
			if method == "PUT" && p[4] == "upload" {
				s.uploadArtifact(w, r, project, "", "", "", "")
				return
			}
		case "environments":
			break
		}
	}
	if len(p) >= 5 && p[3] == "workspaces" {
		ws := p[4]
		if len(p) == 5 && method == "GET" {
			v, e := db.Workspace(project, ws)
			send(w, v, e)
			return
		}
		if len(p) == 6 {
			switch p[5] {
			case "operations":
				if method == "POST" {
					var in struct {
						Action  string         `json:"action"`
						Payload map[string]any `json:"payload"`
					}
					if e := bodyJSON(r, &in); e != nil {
						fail(w, 400, e.Error())
						return
					}
					v, e := db.CallWorkspace(project, ws, in.Action, in.Payload)
					send(w, v, e)
					return
				}
			case "stop":
				if method == "POST" {
					v, e := db.StopWorkspace(project, ws)
					send(w, v, e)
					return
				}
			case "checkpoint-stop":
				if method == "POST" {
					v, e := db.CheckpointStopWorkspace(project, ws)
					send(w, v, e)
					return
				}
				if method == "GET" {
					v, e := db.CheckpointStopStatus(project, ws)
					send(w, v, e)
					return
				}
			case "snapshots":
				if method == "GET" {
					v, e := db.SnapshotList(project, ws)
					send(w, v, e)
					return
				}
			}
		}
		if len(p) == 7 && p[5] == "volume" && p[6] == "prune" && method == "POST" {
			var in struct {
				SnapshotID string `json:"snapshot_id"`
				Confirm    bool   `json:"confirm"`
			}
			if e := bodyJSON(r, &in); e != nil {
				fail(w, 400, e.Error())
				return
			}
			v, e := db.PruneWorkspaceVolume(project, ws, in.SnapshotID, in.Confirm)
			send(w, v, e)
			return
		}
		if len(p) == 7 && p[5] == "operations" && method == "GET" {
			v, e := db.Operation(project, ws, p[6])
			send(w, v, e)
			return
		}
		if len(p) == 7 && p[5] == "artifacts" && p[6] == "import" && method == "POST" {
			var in struct {
				ID   string `json:"artifact_id"`
				Path string `json:"path"`
			}
			if e := bodyJSON(r, &in); e != nil {
				fail(w, 400, e.Error())
				return
			}
			v, e := db.QueueImportFile(project, ws, in.ID, in.Path)
			send(w, v, e)
			return
		}
		if len(p) == 8 && p[5] == "snapshots" && p[7] == "restore" && method == "POST" {
			v, e := db.RestoreSnapshot(project, ws, p[6])
			send(w, v, e)
			return
		}
	}
	if len(p) >= 7 && p[3] == "environments" && method == "POST" {
		action := map[string]string{"build": "build_image", "export": "export_image", "import": "load_image"}[p[5]]
		if action != "" {
			var in struct {
				ID string `json:"artifact_id"`
			}
			if action == "load_image" {
				if e := bodyJSON(r, &in); e != nil {
					fail(w, 400, e.Error())
					return
				}
			}
			v, e := db.EnvironmentAction(project, p[4], p[6], action, in.ID)
			send(w, v, e)
			return
		}
	}
	fail(w, 404, "API route not found")
}
func (s *Server) serveArtifact(w http.ResponseWriter, meta map[string]any, err error) {
	if err != nil {
		send(w, nil, err)
		return
	}
	path, err := s.DB.VerifyArtifact(meta)
	if err != nil {
		fail(w, 500, "artifact checksum failed")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		fail(w, 500, "artifact missing")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-SHA256", asString(meta["sha256"]))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", asString(meta["name"])))
	_, _ = io.Copy(w, f)
}
func (s *Server) uploadArtifact(w http.ResponseWriter, r *http.Request, project, node, ws, op, lease string) {
	name := r.Header.Get("X-Artifact-Name")
	digest := r.Header.Get("X-Content-SHA256")
	meta, e := s.DB.ImportStream(project, name, digest, r.Body, s.MaxArtifactBytes, node, ws, op, lease)
	send(w, meta, e)
}
func (s *Server) handleWorker(w http.ResponseWriter, r *http.Request, p []string) {
	db := s.DB
	node := p[2]
	method := r.Method
	if len(p) == 4 && p[3] == "heartbeat" && method == "POST" {
		var in struct {
			Capabilities map[string]any `json:"capabilities"`
		}
		if e := bodyJSON(r, &in); e != nil {
			fail(w, 400, e.Error())
			return
		}
		e := db.Heartbeat(node, in.Capabilities)
		send(w, map[string]any{"ok": true}, e)
		return
	}
	if len(p) == 4 && p[3] == "reconcile" && method == "POST" {
		v, e := db.ReconcileInterruptedJobs(node)
		send(w, v, e)
		return
	}
	if len(p) == 4 && p[3] == "claim" && method == "POST" {
		v, e := db.ClaimJob(node)
		send(w, map[string]any{"job": v}, e)
		return
	}
	if len(p) == 4 && p[3] == "finish" && method == "POST" {
		var in struct {
			ID     string `json:"job_id"`
			Token  string `json:"lease_token"`
			Status string `json:"status"`
			Result string `json:"result"`
		}
		if e := bodyJSON(r, &in); e != nil {
			fail(w, 400, e.Error())
			return
		}
		v, e := db.FinishJob(node, in.ID, in.Token, in.Status, in.Result)
		send(w, v, e)
		return
	}
	if len(p) == 5 && p[3] == "operation" && method == "POST" {
		switch p[4] {
		case "claim":
			v, e := db.ClaimOperation(node)
			send(w, map[string]any{"operation": v}, e)
			return
		case "renew":
			var in struct {
				ID    string `json:"operation_id"`
				Token string `json:"lease_token"`
			}
			if e := bodyJSON(r, &in); e != nil {
				fail(w, 400, e.Error())
				return
			}
			v, e := db.RenewOperation(node, in.ID, in.Token)
			send(w, v, e)
			return
		case "finish":
			var in struct {
				ID      string         `json:"operation_id"`
				Token   string         `json:"lease_token"`
				Success bool           `json:"success"`
				Result  map[string]any `json:"result"`
			}
			if e := bodyJSON(r, &in); e != nil {
				fail(w, 400, e.Error())
				return
			}
			v, e := db.FinishOperation(node, in.ID, in.Token, in.Success, in.Result)
			send(w, v, e)
			return
		}
	}
	if len(p) == 7 && p[3] == "workspaces" && p[5] == "artifacts" && method == "GET" {
		meta, e := db.WorkerArtifactInfo(node, p[4], p[6])
		s.serveArtifact(w, meta, e)
		return
	}
	if len(p) == 8 && p[3] == "workspaces" && p[5] == "operations" && p[7] == "artifact" && method == "PUT" {
		ws, op := p[4], p[6]
		project, e := db.operationUploadAuthorized(node, ws, op, r.Header.Get("X-Operation-Token"))
		if e != nil {
			fail(w, 403, "operation not authorized for upload")
			return
		}
		s.uploadArtifact(w, r, project, node, ws, op, r.Header.Get("X-Operation-Token"))
		return
	}
	fail(w, 404, "worker endpoint not found")
}

// JSONRequest decodes MCP arguments into the same validated request types as REST.
func JSONRequest(raw any, v any) error {
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}
