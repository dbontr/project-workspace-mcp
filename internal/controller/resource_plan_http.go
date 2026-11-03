package controller

import "net/http"

func (s *Server) handleResourcePlan(w http.ResponseWriter, r *http.Request, p []string) bool {
	if len(p) != 4 || p[0] != "api" || p[1] != "projects" || p[3] != "resource-plan" {
		return false
	}
	if r.Method != http.MethodPost {
		fail(w, 405, "method not allowed")
		return true
	}
	var req WorkspaceRequest
	if err := bodyJSON(r, &req); err != nil {
		fail(w, 400, err.Error())
		return true
	}
	if req.ProjectID != "" && req.ProjectID != p[2] {
		fail(w, 400, "request project does not match scoped resource")
		return true
	}
	req.ProjectID = p[2]
	result, err := s.DB.PlanWorkspace(req)
	send(w, result, err)
	return true
}
