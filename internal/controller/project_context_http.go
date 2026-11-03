package controller

import (
	"net/http"
	"strconv"
)

func (s *Server) handleProjectKnowledge(w http.ResponseWriter, r *http.Request, p []string, who caller) bool {
	if len(p) < 5 || p[0] != "api" || p[1] != "projects" || p[3] != "knowledge" {
		return false
	}
	project := p[2]
	if len(p) == 5 {
		switch p[4] {
		case "context":
			if r.Method != http.MethodGet {
				fail(w, 405, "method not allowed")
				return true
			}
			result, err := s.DB.ProjectContext(project)
			send(w, result, err)
			return true
		case "search":
			if r.Method != http.MethodGet {
				fail(w, 405, "method not allowed")
				return true
			}
			rows, err := s.DB.ProjectContributionSearch(project, r.URL.Query().Get("q"))
			send(w, rows, err)
			return true
		case "contributions":
			switch r.Method {
			case http.MethodGet:
				limit := 50
				if text := r.URL.Query().Get("limit"); text != "" {
					n, err := strconv.Atoi(text)
					if err != nil {
						fail(w, 400, "invalid contribution limit")
						return true
					}
					limit = n
				}
				rows, err := s.DB.ProjectContributions(project, r.URL.Query().Get("kind"), limit)
				send(w, rows, err)
			case http.MethodPost:
				var in ContributionRequest
				if err := bodyJSON(r, &in); err != nil {
					fail(w, 400, err.Error())
					return true
				}
				row, err := s.DB.ContributeProject(project, in, who.Principal)
				send(w, row, err)
			default:
				fail(w, 405, "method not allowed")
			}
			return true
		}
	}
	if len(p) == 6 && p[4] == "contributions" && r.Method == http.MethodGet {
		row, err := s.DB.ProjectContribution(project, p[5])
		send(w, row, err)
		return true
	}
	fail(w, 404, "knowledge endpoint not found")
	return true
}
