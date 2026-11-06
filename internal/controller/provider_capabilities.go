package controller

import "errors"

// Provider capabilities describe what this controller can actually exercise.
// Git object storage is independent of hosted issue and planning services.
func (s *Server) GitProviderCapabilities(provider string) (map[string]any, error) {
	switch provider {
	case "github":
		return map[string]any{
			"provider":                  provider,
			"git_storage":               "supported",
			"knowledge_storage":         "supported",
			"issues":                    "requires-provider-permission",
			"pull_requests":             "requires-provider-permission",
			"project_boards":            "requires-provider-permission",
			"repository_administration": "requires-provider-permission",
			"authorization":             "GitHub credential and project repository grant required",
		}, nil
	case "local-git":
		git := "unconfigured"
		if s.LocalGit != nil {
			git = "supported"
		}
		return map[string]any{
			"provider":                  provider,
			"git_storage":               git,
			"knowledge_storage":         git,
			"issues":                    "unavailable",
			"pull_requests":             "unavailable",
			"project_boards":            "unavailable",
			"repository_administration": "operator-managed",
			"authorization":             "Operator-mounted bare repositories and explicit project grants required",
			"optional_planning_bridge":  "Link an authorized GitHub planning repository separately for Issues, Pull Requests and Projects",
		}, nil
	case "gitlab", "gitea", "remote-git":
		return map[string]any{
			"provider":                  provider,
			"git_storage":               "adapter-required",
			"knowledge_storage":         "adapter-required",
			"issues":                    "adapter-required",
			"pull_requests":             "adapter-required",
			"project_boards":            "adapter-required",
			"repository_administration": "adapter-required",
			"authorization":             "No remote Git or provider REST adapter is configured",
		}, nil
	default:
		return nil, errors.New("unsupported Git provider")
	}
}
