package controller

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
	"strings"
)

var validID = regexp.MustCompile(`^[a-z][a-z0-9-]{1,62}$`)

// Server owns migrated project knowledge endpoints and forwards remaining MCP,
// scheduler, GitHub, and worker routes to the existing loopback controller.
// Only the migrated handlers directly access the shared SQLite database.
type Server struct {
	DB         *Database
	AdminToken string
	Proxy      *httputil.ReverseProxy
}

func NewServer(db *Database, token string, upstream *url.URL) (*Server, error) {
	if len(token) < 32 {
		return nil, errors.New("administrator token must contain at least 32 characters")
	}
	if upstream == nil || upstream.Scheme != "http" || upstream.Host == "" || upstream.Path != "" || upstream.User != nil || upstream.RawQuery != "" || upstream.Fragment != "" || (upstream.Hostname() != "localhost" && upstream.Hostname() != "127.0.0.1") {
		return nil, errors.New("upstream must be a loopback HTTP controller")
	}
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	original := proxy.Director
	proxy.Director = func(req *http.Request) { original(req); req.Host = upstream.Host }
	return &Server{DB: db, AdminToken: token, Proxy: proxy}, nil
}
func (s *Server) authorized(r *http.Request) bool {
	supplied := r.Header.Get("Authorization")
	expected := "Bearer " + s.AdminToken
	return subtle.ConstantTimeCompare([]byte(supplied), []byte(expected)) == 1
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func errorJSON(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"detail": message})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, value any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 600*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("multiple JSON objects not allowed")
	}
	return nil
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, 200, map[string]string{"status": "ok"})
		return
	}
	// Retain the Python MCP and worker endpoints until their protocol and
	// durable scheduler ownership have been migrated together.
	components := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for _, segment := range components {
		if segment == "." || segment == ".." || segment == "" {
			errorJSON(w, 400, "Invalid request path")
			return
		}
	}
	isProjects := r.URL.Path == "/api/projects"
	isNote := len(components) == 5 && components[0] == "api" && components[1] == "projects" && components[3] == "notes"
	isSearch := len(components) == 4 && components[0] == "api" && components[1] == "projects" && components[3] == "search"
	if !isProjects && !isNote && !isSearch {
		s.Proxy.ServeHTTP(w, r)
		return
	}
	if !s.authorized(r) {
		errorJSON(w, 401, "Invalid controller credential")
		return
	}
	if isProjects {
		if r.Method == "GET" {
			rows, err := s.DB.Projects()
			if err != nil {
				errorJSON(w, 500, "Could not load projects")
				return
			}
			writeJSON(w, 200, rows)
			return
		}
		if r.Method == "POST" {
			var input struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			}
			if err := decodeJSON(w, r, &input); err != nil {
				errorJSON(w, 400, "Invalid JSON")
				return
			}
			created, err := s.DB.CreateProject(input.ID, input.Title)
			if err != nil {
				errorJSON(w, 400, err.Error())
				return
			}
			writeJSON(w, 200, created)
			return
		}
	}
	if isNote {
		project, name := components[2], components[4]
		if !validID.MatchString(project) || !validID.MatchString(name) {
			errorJSON(w, 400, "Invalid note path")
			return
		}
		if r.Method == "GET" {
			note, err := s.DB.ReadNote(project, name)
			if err != nil {
				errorJSON(w, 404, "Note not found")
				return
			}
			writeJSON(w, 200, note)
			return
		}
		if r.Method == "POST" {
			var input struct {
				Body            string `json:"body"`
				ExpectedVersion *int   `json:"expected_version"`
			}
			if err := decodeJSON(w, r, &input); err != nil {
				errorJSON(w, 400, "Invalid JSON")
				return
			}
			if input.ExpectedVersion == nil {
				errorJSON(w, 400, "expected_version is required")
				return
			}
			note, err := s.DB.WriteNote(project, name, input.Body, *input.ExpectedVersion)
			if err != nil {
				errorJSON(w, 409, err.Error())
				return
			}
			writeJSON(w, 200, note)
			return
		}
	}
	if isSearch && r.Method == "GET" {
		notes, err := s.DB.SearchNotes(components[2], r.URL.Query().Get("q"))
		if err != nil {
			errorJSON(w, 400, err.Error())
			return
		}
		writeJSON(w, 200, notes)
		return
	}
	errorJSON(w, 405, "Method not allowed")
}
