package controller

import "net/http"

// handlePool serves operator-managed images and package selections. All routes
// are behind the controller's owner-credential boundary.
func (s *Server) handlePool(w http.ResponseWriter, r *http.Request, p []string) bool {
	if len(p) == 3 && p[0] == "api" && p[1] == "pool" {
		switch p[2] {
		case "images":
			switch r.Method {
			case "GET":
				send(w, s.DB.ImagePool())
			case "POST":
				var in struct {
					ID           string `json:"id"`
					Image        string `json:"image"`
					Description  string `json:"description"`
					Architecture string `json:"architecture"`
				}
				if err := bodyJSON(r, &in); err != nil {
					fail(w, 400, err.Error())
					return true
				}
				send(w, s.DB.RegisterPoolImage(in.ID, in.Image, in.Description, in.Architecture))
			default:
				fail(w, 405, "method not allowed")
			}
			return true
		case "software":
			switch r.Method {
			case "GET":
				send(w, s.DB.SoftwarePool())
			case "POST":
				var in struct {
					ID          string   `json:"id"`
					ImageID     string   `json:"image_id"`
					Description string   `json:"description"`
					Packages    []string `json:"packages"`
				}
				if err := bodyJSON(r, &in); err != nil {
					fail(w, 400, err.Error())
					return true
				}
				send(w, s.DB.RegisterPoolSoftware(in.ID, in.ImageID, in.Description, in.Packages))
			default:
				fail(w, 405, "method not allowed")
			}
			return true
		}
	}
	if len(p) == 5 && p[0] == "api" && p[1] == "projects" &&
		p[3] == "environments" && p[4] == "from-pool" {
		if r.Method != "POST" {
			fail(w, 405, "method not allowed")
			return true
		}
		var in struct {
			Name            string   `json:"name"`
			ImageID         string   `json:"image_id"`
			SoftwareIDs     []string `json:"software_ids"`
			ExpectedVersion int      `json:"expected_version"`
		}
		if err := bodyJSON(r, &in); err != nil {
			fail(w, 400, err.Error())
			return true
		}
		send(w, s.DB.EnvironmentFromPool(p[2], in.Name, in.ImageID, in.SoftwareIDs, in.ExpectedVersion))
		return true
	}
	return false
}
