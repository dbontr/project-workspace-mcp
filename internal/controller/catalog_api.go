package controller

import (
	"errors"
	"io"
	"net/http"
	"strconv"
)

// handleCatalog owns project-planning and library routes. All routes pass through
// the controller's owner-authentication boundary before they reach this handler.
func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request, p []string) bool {
	if len(p) >= 2 && p[1] == "libraries" {
		s.handleLibraries(w, r, p)
		return true
	}
	if len(p) < 4 || p[1] != "projects" {
		return false
	}
	project := p[2]
	switch p[3] {
	case "planning":
		s.handlePlanning(w, r, p, project)
		return true
	case "repository-memberships":
		if len(p) == 4 && r.Method == "GET" {
			v, e := s.DB.RepositoryMemberships(project)
			send(w, v, e)
			return true
		}
	case "repositories":
		if len(p) == 4 && r.Method == "POST" {
			var in struct {
				Repository string `json:"repository"`
				Role       string `json:"role"`
				Ref        string `json:"default_ref"`
			}
			if e := bodyJSON(r, &in); e != nil {
				fail(w, 400, e.Error())
				return true
			}
			v, e := s.DB.LinkRepositoryWithRole(project, in.Repository, in.Role, in.Ref)
			send(w, v, e)
			return true
		}
	case "libraries":
		if len(p) == 4 {
			switch r.Method {
			case "GET":
				v, e := s.DB.ProjectLibraries(project)
				send(w, v, e)
			case "POST":
				var in struct {
					Library string `json:"library"`
				}
				if e := bodyJSON(r, &in); e != nil {
					fail(w, 400, e.Error())
					break
				}
				v, e := s.DB.LinkLibrary(project, in.Library)
				send(w, v, e)
			default:
				fail(w, 405, "method not allowed")
			}
			return true
		}
		if len(p) == 6 && p[5] == "resolve" && r.Method == "GET" {
			v, e := s.DB.ResolveLibrary(project, p[4])
			send(w, v, e)
			return true
		}
		if len(p) == 6 && p[5] == "sources" && r.Method == "GET" {
			if !s.DB.libraryAttached(project, p[4]) {
				send(w, nil, errors.New("library not linked to project"))
				return true
			}
			repo, path, file := r.URL.Query().Get("repository"), r.URL.Query().Get("source_path"), r.URL.Query().Get("file")
			source, e := s.DB.LibrarySource(p[4], repo, path)
			if e != nil {
				send(w, nil, e)
				return true
			}
			if !libraryFileAllowed(file, path) {
				fail(w, 400, "file is outside the pinned library source")
				return true
			}
			v, e := s.GitHub.File(repo, file, asString(source["revision"]))
			send(w, v, e)
			return true
		}
		if len(p) == 6 && p[5] == "checkout" && r.Method == "POST" {
			var in struct {
				WorkspaceID string `json:"workspace_id"`
				Repository  string `json:"repository"`
				SourcePath  string `json:"source_path"`
			}
			if e := bodyJSON(r, &in); e != nil {
				fail(w, 400, e.Error())
				return true
			}
			v, e := s.CheckoutLibrarySource(project, in.WorkspaceID, p[4], in.Repository, in.SourcePath)
			send(w, v, e)
			return true
		}
		if len(p) == 6 && p[5] == "import" && r.Method == "POST" {
			var in struct {
				WorkspaceID string `json:"workspace_id"`
				Name        string `json:"name"`
				Path        string `json:"path"`
			}
			if e := bodyJSON(r, &in); e != nil {
				fail(w, 400, e.Error())
				return true
			}
			v, e := s.DB.QueueLibraryObjectImport(project, in.WorkspaceID, p[4], in.Name, in.Path)
			send(w, v, e)
			return true
		}
	}
	return false
}
func (s *Server) handlePlanning(w http.ResponseWriter, r *http.Request, p []string, project string) {
	if len(p) == 4 {
		switch r.Method {
		case "GET":
			v, e := s.DB.PlanningBoard(project)
			send(w, v, e)
		case "POST":
			var in struct {
				ProjectID string `json:"github_project_id"`
				URL       string `json:"url"`
			}
			if e := bodyJSON(r, &in); e != nil {
				fail(w, 400, e.Error())
				return
			}
			if e := requireID(project); e != nil {
				send(w, nil, e)
				return
			}
			projectRow, e := s.DB.queryOne(`SELECT id FROM projects WHERE id=?`, project)
			if e != nil {
				send(w, nil, e)
				return
			}
			if projectRow == nil {
				fail(w, 404, "project not found")
				return
			}
			var meta map[string]any
			if (in.ProjectID == "") == (in.URL == "") {
				fail(w, 400, "specify one GitHub Project ID or URL")
				return
			}
			if in.URL != "" {
				meta, e = s.GitHub.ProjectByURL(in.URL)
			} else {
				meta, e = s.GitHub.ProjectMetadata(in.ProjectID)
			}
			if e != nil {
				send(w, nil, e)
				return
			}
			v, e := s.DB.AttachPlanningBoard(project, meta)
			send(w, v, e)
		default:
			fail(w, 405, "method not allowed")
		}
		return
	}
	board, e := s.DB.PlanningBoard(project)
	if e != nil {
		send(w, nil, e)
		return
	}
	id := asString(board["github_id"])
	if len(p) == 5 {
		switch p[4] {
		case "items":
			if r.Method == "GET" {
				v, e := s.GitHub.ProjectItems(id, r.URL.Query().Get("after"))
				send(w, v, e)
				return
			}
			if r.Method == "POST" {
				var in struct {
					Repository string `json:"repository"`
					Kind       string `json:"kind"`
					Number     int    `json:"number"`
				}
				if e := bodyJSON(r, &in); e != nil {
					fail(w, 400, e.Error())
					return
				}
				if !s.DB.RepositoryLinked(project, in.Repository) {
					send(w, nil, errors.New("repository is not linked to this project"))
					return
				}
				v, e := s.GitHub.ProjectAddIssue(id, in.Repository, in.Kind, in.Number)
				send(w, v, e)
				return
			}
		case "drafts":
			if r.Method == "POST" {
				var in struct {
					Title string `json:"title"`
					Body  string `json:"body"`
				}
				if e := bodyJSON(r, &in); e != nil {
					fail(w, 400, e.Error())
					return
				}
				v, e := s.GitHub.ProjectAddDraft(id, in.Title, in.Body)
				send(w, v, e)
				return
			}
		case "fields":
			if r.Method == "GET" {
				v, e := s.GitHub.ProjectFields(id, r.URL.Query().Get("after"))
				send(w, v, e)
				return
			}
			if r.Method == "POST" {
				var in struct {
					Item   string `json:"item_id"`
					Field  string `json:"field_id"`
					Option string `json:"option_id"`
				}
				if e := bodyJSON(r, &in); e != nil {
					fail(w, 400, e.Error())
					return
				}
				v, e := s.GitHub.ProjectSetSingleSelect(id, in.Item, in.Field, in.Option)
				send(w, v, e)
				return
			}
		}
	}
	fail(w, 404, "planning route not found")
}
func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request, p []string) {
	db := s.DB
	if len(p) == 2 {
		switch r.Method {
		case "GET":
			v, e := db.Libraries()
			send(w, v, e)
		case "POST":
			var in struct {
				ID          string `json:"id"`
				Title       string `json:"title"`
				Description string `json:"description"`
				Kind        string `json:"kind"`
			}
			if e := bodyJSON(r, &in); e != nil {
				fail(w, 400, e.Error())
				return
			}
			v, e := db.CreateLibrary(in.ID, in.Title, in.Description, in.Kind)
			send(w, v, e)
		default:
			fail(w, 405, "method not allowed")
		}
		return
	}
	id := p[2]
	if len(p) == 3 && r.Method == "GET" {
		v, e := db.Library(id)
		send(w, v, e)
		return
	}
	if len(p) == 4 && p[3] == "uploads" && r.Method == "POST" {
		var in struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
			Sha  string `json:"sha256"`
			Size int64  `json:"size"`
		}
		if e := bodyJSON(r, &in); e != nil {
			fail(w, 400, e.Error())
			return
		}
		v, e := db.StartLibraryUpload(id, in.Name, in.Kind, in.Sha, in.Size, s.MaxArtifactBytes)
		send(w, v, e)
		return
	}
	if len(p) == 5 && p[3] == "uploads" {
		switch r.Method {
		case "GET":
			v, e := db.uploadRow(id, p[4])
			send(w, v, e)
		case "PUT":
			offset, e := strconv.ParseInt(r.Header.Get("X-Upload-Offset"), 10, 64)
			if e != nil {
				fail(w, 400, "X-Upload-Offset must be a decimal byte offset")
				return
			}
			v, e := db.AppendLibraryChunk(id, p[4], offset, r.Body)
			send(w, v, e)
		case "DELETE":
			if e := db.CancelLibraryUpload(id, p[4]); e != nil {
				send(w, nil, e)
			} else {
				reply(w, 200, map[string]any{"cancelled": true})
			}
		default:
			fail(w, 405, "method not allowed")
		}
		return
	}
	if len(p) == 6 && p[3] == "uploads" && p[5] == "complete" && r.Method == "POST" {
		v, e := db.CompleteLibraryUpload(id, p[4])
		send(w, v, e)
		return
	}
	if len(p) == 4 {
		switch p[3] {
		case "sources":
			if r.Method == "GET" {
				v, e := db.LibrarySources(id)
				send(w, v, e)
				return
			}
			if r.Method == "POST" {
				var in struct {
					Repository string `json:"repository"`
					Ref        string `json:"ref"`
					Path       string `json:"path"`
					Kind       string `json:"kind"`
				}
				if e := bodyJSON(r, &in); e != nil {
					fail(w, 400, e.Error())
					return
				}
				if _, e := db.Library(id); e != nil {
					send(w, nil, e)
					return
				}
				revision, e := s.GitHub.Commit(in.Repository, in.Ref)
				if e != nil {
					send(w, nil, e)
					return
				}
				v, e := db.AddLibrarySource(id, in.Repository, revision, in.Path, in.Kind)
				send(w, v, e)
				return
			}
		case "objects":
			if r.Method == "GET" {
				v, e := db.LibraryObjects(id)
				send(w, v, e)
				return
			}
		}
	}
	if len(p) == 5 && p[3] == "objects" {
		name := p[4]
		switch r.Method {
		case "PUT":
			v, e := db.StoreLibraryObject(id, name, r.Header.Get("X-Object-Kind"), r.Header.Get("X-Content-SHA256"), r.Body, s.MaxArtifactBytes)
			send(w, v, e)
		case "GET":
			file, meta, e := db.OpenLibraryObject(id, name)
			if e != nil {
				send(w, nil, e)
				return
			}
			defer file.Close()
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("X-Content-SHA256", asString(meta["sha256"]))
			w.Header().Set("Content-Length", strconv.FormatInt(int64(asFloat(meta["size"])), 10))
			w.Header().Set("Content-Disposition", "attachment")
			_, _ = io.Copy(w, file)
		default:
			fail(w, 405, "method not allowed")
		}
		return
	}
	fail(w, 404, "library route not found")
}
