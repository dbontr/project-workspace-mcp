package controller

import (
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

//go:embed dashboard.html
var dashboard embed.FS

type Server struct {
	DB               *Database
	AdminToken       string
	OIDC             *OIDCVerifier
	AccessVerifier   *OIDCVerifier
	AccessOwnerEmail string
	PublicURL        string
	GitHub           *GitHub
	LocalGit         *LocalGit
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
	verifier, err := configuredOIDC()
	if err != nil {
		return nil, err
	}
	access, accessOwner, err := configuredAccessVerifier()
	if err != nil {
		return nil, err
	}
	publicURL := strings.TrimRight(os.Getenv("PWM_PUBLIC_URL"), "/")
	if publicURL != "" {
		parsed, err := url.Parse(publicURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
			return nil, errors.New("PWM_PUBLIC_URL must be an HTTPS origin")
		}
	}
	pat, err := configuredGitHubPAT()
	if err != nil {
		return nil, err
	}
	github := NewGitHub(pat)
	if pat == "" {
		if err := configureGitHubAppFromEnv(github); err != nil {
			return nil, err
		}
	}
	github.GenericWrites = os.Getenv("PWM_GITHUB_GENERIC_WRITES") == "1"
	var localGit *LocalGit
	if configuredRoot := os.Getenv("PWM_GIT_LOCAL_ROOT"); configuredRoot != "" {
		if !filepath.IsAbs(configuredRoot) {
			return nil, errors.New("PWM_GIT_LOCAL_ROOT must be an absolute path")
		}
		root, err := filepath.EvalSymlinks(configuredRoot)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(root)
		if err != nil || !info.IsDir() {
			return nil, errors.New("PWM_GIT_LOCAL_ROOT must be a directory")
		}
		localGit = &LocalGit{
			Root:        root,
			AuthorName:  strings.TrimSpace(os.Getenv("PWM_GIT_AUTHOR_NAME")),
			AuthorEmail: strings.TrimSpace(os.Getenv("PWM_GIT_AUTHOR_EMAIL")),
		}
	}
	return &Server{DB: db, AdminToken: token, OIDC: verifier, AccessVerifier: access, AccessOwnerEmail: accessOwner, PublicURL: publicURL,
		GitHub: github, LocalGit: localGit,
		AllowedHosts:     configured("PWM_ALLOWED_HOSTS", "127.0.0.1,localhost"),
		AllowedOrigins:   configured("PWM_ALLOWED_ORIGINS", "http://127.0.0.1,http://localhost"),
		MaxArtifactBytes: limit}, nil
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
	if r.URL.Path == "/.well-known/oauth-protected-resource" {
		if s.OIDC == nil || s.PublicURL == "" {
			fail(w, 404, "OAuth resource metadata is not configured")
			return
		}
		if r.Method != http.MethodGet {
			fail(w, 405, "method not allowed")
			return
		}
		reply(w, 200, map[string]any{"resource": s.PublicURL, "authorization_servers": []string{s.OIDC.Issuer}, "bearer_methods_supported": []string{"header"}})
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
		who, err := s.authenticate(r)
		if err != nil {
			s.unauthorized(w)
			return
		}
		s.handleMCP(w, r, who)
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
	who, err := s.authenticate(r)
	if err != nil {
		s.unauthorized(w)
		return
	}
	if !s.authorizeREST(r, parts, who) {
		fail(w, 403, "project access denied")
		return
	}
	if s.handleAccess(w, r, parts, who) {
		return
	}
	if s.handlePersonalTaskClaims(w, r, parts, who) {
		return
	}
	if s.handlePersonalTasks(w, r, parts, who) {
		return
	}
	if s.handleProjectKnowledge(w, r, parts, who) {
		return
	}
	if s.handleResourcePlan(w, r, parts) {
		return
	}
	if s.handleTaskClaims(w, r, parts) {
		return
	}
	if s.handlePool(w, r, parts) {
		return
	}
	if s.handleGitHubWorkflow(w, r, parts) {
		return
	}
	if s.handleResearch(w, r, parts) {
		return
	}
	if s.handleCatalog(w, r, parts) {
		return
	}
	s.handleAdmin(w, r, parts)
}

func (s *Server) unauthorized(w http.ResponseWriter) {
	challenge := "Bearer realm=\"project-workspace\""
	if s.PublicURL != "" && s.OIDC != nil {
		challenge += ", resource_metadata=\"" + s.PublicURL + "/.well-known/oauth-protected-resource\""
	}
	w.Header().Set("WWW-Authenticate", challenge)
	fail(w, 401, "invalid controller credential")
}
