package controller

import "net/http"

func (s *Server) handlePersonalTaskClaims(w http.ResponseWriter, r *http.Request, p []string, who caller) bool {
	if len(p) < 4 || p[0] != "api" || p[1] != "tasks" || p[3] != "claims" {
		return false
	}
	task := p[2]
	owner := who.Principal
	if len(p) == 4 {
		switch r.Method {
		case http.MethodGet:
			rows, err := s.DB.PersonalTaskClaimList(owner, task)
			send(w, rows, err)
		case http.MethodPost:
			var in struct {
				WorkKey      string `json:"work_key"`
				LeaseSeconds int    `json:"lease_seconds"`
			}
			if err := bodyJSON(r, &in); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			if in.LeaseSeconds == 0 {
				in.LeaseSeconds = 900
			}
			result, err := s.DB.ClaimPersonalTask(owner, task, in.WorkKey, in.LeaseSeconds)
			send(w, result, err)
		default:
			fail(w, 405, "method not allowed")
		}
		return true
	}
	if len(p) == 5 && r.Method == http.MethodGet {
		row, err := s.DB.PersonalTaskClaimStatus(owner, task, p[4])
		send(w, row, err)
		return true
	}
	if len(p) == 6 && r.Method == http.MethodPost {
		switch p[5] {
		case "renew":
			var in struct {
				LeaseToken   string `json:"lease_token"`
				Generation   int    `json:"generation"`
				LeaseSeconds int    `json:"lease_seconds"`
			}
			if err := bodyJSON(r, &in); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			if in.LeaseSeconds == 0 {
				in.LeaseSeconds = 900
			}
			row, err := s.DB.RenewPersonalTaskClaim(owner, task, p[4], in.LeaseToken, in.Generation, in.LeaseSeconds)
			send(w, row, err)
			return true
		case "finish":
			var in struct {
				LeaseToken  string   `json:"lease_token"`
				Generation  int      `json:"generation"`
				Status      string   `json:"status"`
				ArtifactIDs []string `json:"artifact_ids"`
			}
			if err := bodyJSON(r, &in); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			row, err := s.DB.FinishPersonalTaskClaim(owner, task, p[4], in.LeaseToken, in.Status, in.Generation, in.ArtifactIDs)
			send(w, row, err)
			return true
		}
	}
	fail(w, 405, "unsupported personal task claim operation")
	return true
}
