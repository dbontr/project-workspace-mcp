package controller

import "net/http"

// handleTaskClaims manages project-scoped cooperative work leases. The
// controller's existing access-control middleware authorizes these routes.
func (s *Server) handleTaskClaims(w http.ResponseWriter, r *http.Request, p []string) bool {
	if len(p) < 4 || p[0] != "api" || p[1] != "projects" || p[3] != "claims" {
		return false
	}
	project := p[2]
	if len(p) == 4 {
		switch r.Method {
		case http.MethodGet:
			rows, err := s.DB.ListTaskClaims(project)
			send(w, rows, err)
		case http.MethodPost:
			var req struct {
				WorkKey      string `json:"work_key"`
				LeaseSeconds int    `json:"lease_seconds"`
			}
			if err := bodyJSON(r, &req); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			if req.LeaseSeconds == 0 {
				req.LeaseSeconds = 900
			}
			result, err := s.DB.ClaimTask(project, req.WorkKey, req.LeaseSeconds)
			send(w, result, err)
		default:
			fail(w, 405, "method not allowed")
		}
		return true
	}
	if len(p) == 5 && r.Method == http.MethodGet {
		row, err := s.DB.TaskClaimStatus(project, p[4])
		send(w, row, err)
		return true
	}
	if len(p) == 6 && r.Method == http.MethodPost {
		var req struct {
			LeaseToken   string   `json:"lease_token"`
			Generation   int      `json:"generation"`
			LeaseSeconds int      `json:"lease_seconds"`
			Status       string   `json:"status"`
			ArtifactIDs  []string `json:"artifact_ids"`
		}
		if err := bodyJSON(r, &req); err != nil {
			fail(w, 400, err.Error())
			return true
		}
		switch p[5] {
		case "renew":
			if req.LeaseSeconds == 0 {
				req.LeaseSeconds = 900
			}
			result, err := s.DB.RenewTaskClaim(project, p[4], req.LeaseToken, req.Generation, req.LeaseSeconds)
			send(w, result, err)
			return true
		case "finish":
			result, err := s.DB.FinishTaskClaim(project, p[4], req.LeaseToken, req.Status, req.Generation, req.ArtifactIDs)
			send(w, result, err)
			return true
		}
	}
	fail(w, 404, "task claim endpoint not found")
	return true
}
