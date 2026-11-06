package controller

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
)

func personalRevision(r *http.Request) (int, error) {
	if text := r.URL.Query().Get("revision"); text != "" {
		return strconv.Atoi(text)
	}
	return 0, nil
}

func (s *Server) handlePersonalTasks(w http.ResponseWriter, r *http.Request, p []string, who caller) bool {
	if len(p) < 2 || p[0] != "api" || p[1] != "tasks" {
		return false
	}
	owner := who.Principal
	if len(p) == 2 {
		switch r.Method {
		case http.MethodGet:
			rows, err := s.DB.PersonalTasks(owner, r.URL.Query().Get("q"))
			send(w, rows, err)
		case http.MethodPost:
			var in struct {
				Title       string   `json:"title"`
				Description string   `json:"description"`
				Tags        []string `json:"tags"`
			}
			if err := bodyJSON(r, &in); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			row, err := s.DB.CreatePersonalTask(owner, in.Title, in.Description, in.Tags)
			send(w, row, err)
		default:
			fail(w, 405, "method not allowed")
		}
		return true
	}
	task := p[2]
	if len(p) == 3 {
		switch r.Method {
		case http.MethodGet:
			row, err := s.DB.PersonalTaskInfo(owner, task)
			send(w, row, err)
		case http.MethodPatch:
			var in struct {
				Title           string   `json:"title"`
				Description     string   `json:"description"`
				Tags            []string `json:"tags"`
				Status          string   `json:"status"`
				ExpectedVersion int      `json:"expected_version"`
			}
			if err := bodyJSON(r, &in); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			row, err := s.DB.UpdatePersonalTask(owner, task, in.Title, in.Description, in.Status, in.Tags, in.ExpectedVersion)
			send(w, row, err)
		default:
			fail(w, 405, "method not allowed")
		}
		return true
	}
	if len(p) == 4 && p[3] == "compute" {
		switch r.Method {
		case http.MethodGet:
			rows, err := s.DB.PersonalTaskJobs(owner, task)
			send(w, rows, err)
		case http.MethodPost:
			var in struct {
				Command        string         `json:"command"`
				Image          string         `json:"image"`
				CPU            float64        `json:"cpu"`
				MemoryMB       int            `json:"memory_mb"`
				Hardware       map[string]any `json:"hardware"`
				MinGPUMemoryMB int            `json:"min_gpu_memory_mb"`
			}
			if err := bodyJSON(r, &in); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			if in.Image == "" {
				in.Image = "debian:bookworm-slim"
			}
			if in.CPU == 0 {
				in.CPU = 1
			}
			if in.MemoryMB == 0 {
				in.MemoryMB = 512
			}
			row, err := s.DB.SubmitPersonalTaskJobWithHardware(owner, task, in.Command, in.Image, in.CPU, in.MemoryMB,
				in.Hardware, in.MinGPUMemoryMB)
			send(w, row, err)
		default:
			fail(w, 405, "method not allowed")
		}
		return true
	}
	if p[3] != "files" {
		fail(w, 404, "task route not found")
		return true
	}
	if len(p) == 4 && r.Method == http.MethodGet {
		rows, err := s.DB.PersonalTaskFiles(owner, task)
		send(w, rows, err)
		return true
	}
	if len(p) < 5 {
		fail(w, 404, "task route not found")
		return true
	}
	name := p[4]
	if len(p) == 5 {
		switch r.Method {
		case http.MethodGet:
			revision, err := personalRevision(r)
			if err != nil {
				fail(w, 400, "invalid file revision")
				return true
			}
			row, err := s.DB.PersonalTaskFile(owner, task, name, revision)
			send(w, row, err)
		case http.MethodPut:
			revision, err := strconv.Atoi(r.Header.Get("X-Expected-Revision"))
			if err != nil {
				fail(w, 400, "expected file revision required")
				return true
			}
			row, err := s.DB.SavePersonalTaskStream(owner, task, name,
				r.Header.Get("X-Content-SHA256"), r.Body, s.MaxArtifactBytes, revision)
			send(w, row, err)
		default:
			fail(w, 405, "method not allowed")
		}
		return true
	}
	if len(p) == 6 {
		switch p[5] {
		case "download":
			if r.Method != http.MethodGet {
				fail(w, 405, "method not allowed")
				return true
			}
			revision, err := personalRevision(r)
			if err != nil {
				fail(w, 400, "invalid file revision")
				return true
			}
			file, row, err := s.DB.OpenPersonalTaskFile(owner, task, name, revision)
			if err != nil {
				fail(w, 400, err.Error())
				return true
			}
			defer file.Close()
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
			w.Header().Set("X-Content-SHA256", asString(row["sha256"]))
			w.Header().Set("Cache-Control", "no-store")
			_, _ = io.Copy(w, file)
			return true
		case "promote":
			if r.Method != http.MethodPost {
				fail(w, 405, "method not allowed")
				return true
			}
			var in struct {
				ProjectID string `json:"project_id"`
				Revision  int    `json:"revision"`
			}
			if err := bodyJSON(r, &in); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			if !s.authorizedFor(in.ProjectID, who, 2) {
				fail(w, 403, "project editor permission required")
				return true
			}
			row, err := s.DB.PromotePersonalTaskFile(owner, task, name, in.Revision, in.ProjectID)
			send(w, row, err)
			return true
		}
	}
	fail(w, 404, "task route not found")
	return true
}
