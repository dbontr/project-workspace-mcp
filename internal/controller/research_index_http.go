package controller

import (
	"net/http"
	"strconv"
)

func (s *Server) handleResearch(w http.ResponseWriter, r *http.Request, p []string) bool {
	if len(p) >= 5 && p[0] == "api" && p[1] == "libraries" &&
		p[3] == "research" && p[4] == "sources" {
		library := p[2]
		if len(p) == 5 && r.Method == http.MethodPost {
			var in ResearchSourceRequest
			if err := bodyJSON(r, &in); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			if in.Library != "" && in.Library != library {
				fail(w, 400, "research source library mismatch")
				return true
			}
			in.Library = library
			result, err := s.DB.RegisterResearchSource(in)
			send(w, result, err)
			return true
		}
		if len(p) == 7 && p[6] == "passages" && r.Method == http.MethodPost {
			var in struct {
				Mode             string                 `json:"mode"`
				ExpectedRevision int                    `json:"expected_revision"`
				Passages         []ResearchPassageInput `json:"passages"`
			}
			if err := bodyJSON(r, &in); err != nil {
				fail(w, 400, err.Error())
				return true
			}
			result, err := s.DB.IngestResearchPassages(library, p[5], in.Mode, in.ExpectedRevision, in.Passages)
			send(w, result, err)
			return true
		}
		fail(w, 405, "unsupported research source operation")
		return true
	}
	if len(p) < 5 || p[0] != "api" || p[1] != "projects" || p[3] != "research" {
		return false
	}
	project := p[2]
	if r.Method != http.MethodGet {
		fail(w, 405, "research search is read-only for project clients")
		return true
	}
	switch p[4] {
	case "topics":
		if len(p) == 5 {
			v, err := s.DB.ResearchTopics(project)
			send(w, v, err)
			return true
		}
		if len(p) == 6 {
			limit := 30
			if text := r.URL.Query().Get("limit"); text != "" {
				n, err := strconv.Atoi(text)
				if err != nil {
					fail(w, 400, "invalid topic result limit")
					return true
				}
				limit = n
			}
			v, err := s.DB.ResearchTopicPassages(project, p[5], limit)
			send(w, v, err)
			return true
		}
	case "sources":
		if len(p) == 5 {
			v, err := s.DB.ResearchSources(project)
			send(w, v, err)
			return true
		}
	case "search":
		if len(p) == 5 {
			limit := 30
			if text := r.URL.Query().Get("limit"); text != "" {
				n, err := strconv.Atoi(text)
				if err != nil {
					fail(w, 400, "invalid research search limit")
					return true
				}
				limit = n
			}
			v, err := s.DB.SearchResearch(project, r.URL.Query().Get("q"), limit)
			send(w, v, err)
			return true
		}
	case "passages":
		if len(p) == 6 {
			v, err := s.DB.ReadResearchPassage(project, p[5])
			send(w, v, err)
			return true
		}
	}
	fail(w, 404, "research endpoint not found")
	return true
}
