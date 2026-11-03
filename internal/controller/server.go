package controller

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
)

//go:embed dashboard.html
var dashboard embed.FS

type Server struct {
	DB               *Database
	AdminToken       string
	GitHub           *GitHub
	AllowedHosts     map[string]bool
	AllowedOrigins   map[string]bool
	MaxArtifactBytes int64
}

func configured(what, defaults string) map[string]bool {
	raw := os.Getenv(what)
	if raw == "" {
		raw = defaults
	}
	set := map[string]bool{}
	for _, value := range strings.Split(raw, ",") {
		value = strings.TrimSpace(value)
		if value != "" {
			set[value] = true
		}
	}
	return set
}
func NewServer(db *Database, token string) (*Server, error) {
	if db == nil || len(token) < 32 {
		return nil, errors.New("controller requires a database and a 32-character administrator secret")
	}
	limit := int64(2) << 30
	if v := os.Getenv("PWM_MAX_ARTIFACT_BYTES"); v != "" {
		n, e := strconv.ParseInt(v, 10, 64)
		if e != nil || n < 1 || n > 100<<30 {
			return nil, errors.New("invalid artifact storage limit")
		}
		limit = n
	}
	github := NewGitHub(os.Getenv("PWM_GITHUB_TOKEN"))
	github.GenericWrites = os.Getenv("PWM_GITHUB_GENERIC_WRITES") == "1"
	return &Server{DB: db, AdminToken: token, GitHub: github, AllowedHosts: configured("PWM_ALLOWED_HOSTS", "127.0.0.1,localhost"), AllowedOrigins: configured("PWM_ALLOWED_ORIGINS", "http://127.0.0.1,http://localhost"), MaxArtifactBytes: limit}, nil
}
func (s *Server) isAuthorized(r *http.Request) bool {
	return subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+s.AdminToken)) == 1
}
func (s *Server) workerAuth(r *http.Request, node string) bool {
	header := r.Header.Get("Authorization")
	return strings.HasPrefix(header, "Bearer ") && s.DB.NodeAuthorized(node, strings.TrimPrefix(header, "Bearer "))
}
func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func fail(w http.ResponseWriter, status int, message string) {
	reply(w, status, map[string]any{"detail": message})
}
func send(w http.ResponseWriter, result any, err error) {
	if err == nil {
		reply(w, 200, result)
		return
	}
	msg := err.Error()
	status := 400
	switch {
	case strings.Contains(strings.ToLower(msg), "conflict"):
		status = 409
	case strings.Contains(strings.ToLower(msg), "not found"):
		status = 404
	case strings.Contains(strings.ToLower(msg), "unauthorized"), strings.Contains(strings.ToLower(msg), "lease"):
		status = 403
	}
	fail(w, status, msg)
}
func bodyJSON(r *http.Request, value any) error {
	if r.Body == nil {
		return errors.New("JSON body required")
	}
	defer r.Body.Close()
	dec := json.NewDecoder(io.LimitReader(r.Body, 3<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return errors.New("extra JSON data")
	}
	return nil
}
func hostAllowed(r *http.Request, allowed map[string]bool) bool {
	h := r.Host
	if allowed[h] || allowed["*"] {
		return true
	}
	host, _, err := net.SplitHostPort(h)
	if err == nil {
		return allowed[host] || allowed[host+":*"]
	}
	return false
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !hostAllowed(r, s.AllowedHosts) {
		fail(w, 400, "unapproved HTTP host")
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if origin := r.Header.Get("Origin"); origin != "" && !s.AllowedOrigins[origin] {
		fail(w, 403, "unapproved origin")
		return
	}
	if r.URL.Path == "/health" {
		if r.Method != "GET" {
			fail(w, 405, "method not allowed")
			return
		}
		reply(w, 200, map[string]any{"status": "ok"})
		return
	}
	if r.URL.Path == "/dashboard" {
		if r.Method != "GET" {
			fail(w, 405, "method not allowed")
			return
		}
		data, _ := dashboard.ReadFile("dashboard.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; connect-src 'self'; form-action 'self'; base-uri 'none'")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
		return
	}
	if r.URL.Path == "/mcp" || r.URL.Path == "/mcp/" {
		if !s.isAuthorized(r) {
			fail(w, 401, "unauthorized")
			return
		}
		s.handleMCP(w, r)
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/api/") {
		fail(w, 404, "not found")
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	for _, segment := range parts {
		if segment == "" || segment == "." || segment == ".." {
			fail(w, 400, "invalid request path")
			return
		}
	}
	if len(parts) >= 3 && parts[1] == "worker" {
		if !s.workerAuth(r, parts[2]) {
			fail(w, 401, "invalid worker credential")
			return
		}
		s.handleWorker(w, r, parts)
		return
	}
	if len(parts) == 2 && parts[1] == "pair" {
		s.handlePair(w, r)
		return
	}
	if !s.isAuthorized(r) {
		fail(w, 401, "invalid controller credential")
		return
	}
	if s.handleGitHubWorkflow(w, r, parts) {
		return
	}
	if s.handleCatalog(w, r, parts) {
		return
	}
	s.handleAdmin(w, r, parts)
}
