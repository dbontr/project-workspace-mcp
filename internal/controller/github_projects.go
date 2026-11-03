package controller

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var validGraphID = regexp.MustCompile(`^[A-Za-z0-9_=-]{8,200}$`)

const planningMetadataQuery = `query($id: ID!) { node(id: $id) { __typename ... on ProjectV2 { id title url readme shortDescription number } } }`
const planningFieldsQuery = `query($id: ID!, $after: String) {
  node(id: $id) { __typename ... on ProjectV2 {
    id url fields(first: 50, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes {
        ... on ProjectV2Field { id name dataType }
        ... on ProjectV2SingleSelectField { id name options { id name } }
        ... on ProjectV2IterationField { id name }
      }
    }
  } }
}`

const planningItemsQuery = `query($id: ID!, $after: String) {
  node(id: $id) { __typename ... on ProjectV2 {
    id title url readme shortDescription number
    items(first: 50, after: $after) { pageInfo { hasNextPage endCursor } nodes {
      id type content {
        ... on DraftIssue { title body }
        ... on Issue { id number title url state repository { nameWithOwner } }
        ... on PullRequest { id number title url state repository { nameWithOwner } }
      }
      fieldValues(first: 30) { nodes {
        ... on ProjectV2ItemFieldTextValue { text field { ... on ProjectV2FieldCommon { name } } }
        ... on ProjectV2ItemFieldSingleSelectValue { name field { ... on ProjectV2FieldCommon { name } } }
        ... on ProjectV2ItemFieldDateValue { date field { ... on ProjectV2FieldCommon { name } } }
        ... on ProjectV2ItemFieldNumberValue { number field { ... on ProjectV2FieldCommon { name } } }
      } }
    } }
  } }
}`

// GraphQL deliberately uses the same restricted GitHub origin and redirect policy
// as the REST client. Variables carry all user-controlled values.
func (g *GitHub) GraphQL(query string, variables map[string]any) (map[string]any, error) {
	v, e := g.request("POST", "/graphql", nil, map[string]any{"query": query, "variables": variables})
	if e != nil {
		return nil, e
	}
	result := gitObj(v)
	if errs, ok := result["errors"].([]any); ok && len(errs) > 0 {
		first := gitObj(errs[0])
		message := asString(first["message"])
		if len(message) > 256 {
			message = message[:256]
		}
		return nil, fmt.Errorf("GitHub Projects GraphQL: %s", message)
	}
	data, ok := result["data"].(map[string]any)
	if !ok || data == nil {
		return nil, errors.New("GitHub GraphQL response missing data")
	}
	return data, nil
}
func githubProjectURL(raw string) bool {
	parsed, e := url.Parse(raw)
	if e != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return false
	}
	return regexp.MustCompile(`^/(users|orgs)/[A-Za-z0-9_.-]+/projects/[0-9]+$`).MatchString(parsed.Path)
}
func (g *GitHub) ProjectMetadata(id string) (map[string]any, error) {
	if !validGraphID.MatchString(id) {
		return nil, errors.New("invalid GitHub Project node ID")
	}
	data, e := g.GraphQL(planningMetadataQuery, map[string]any{"id": id})
	if e != nil {
		return nil, e
	}
	node := gitObj(data["node"])
	if node["__typename"] != "ProjectV2" || node["id"] != id || !githubProjectURL(asString(node["url"])) {
		return nil, errors.New("GitHub Project not found or invalid")
	}
	return node, nil
}
func (g *GitHub) ProjectItems(id, cursor string) (map[string]any, error) {
	if !validGraphID.MatchString(id) || len(cursor) > 500 {
		return nil, errors.New("invalid GitHub Project request")
	}
	var after any
	if cursor != "" {
		after = cursor
	}
	data, e := g.GraphQL(planningItemsQuery, map[string]any{"id": id, "after": after})
	if e != nil {
		return nil, e
	}
	node := gitObj(data["node"])
	if node["__typename"] != "ProjectV2" || node["id"] != id || !githubProjectURL(asString(node["url"])) {
		return nil, errors.New("GitHub Project not found")
	}
	return node, nil
}

// ProjectFields exposes GitHub-owned field and option IDs so clients can
// update status without guessing identifiers or mirroring the Project schema.
func (g *GitHub) ProjectFields(id, cursor string) (map[string]any, error) {
	if !validGraphID.MatchString(id) || len(cursor) > 500 {
		return nil, errors.New("invalid GitHub Project request")
	}
	var after any
	if cursor != "" {
		after = cursor
	}
	data, err := g.GraphQL(planningFieldsQuery, map[string]any{"id": id, "after": after})
	if err != nil {
		return nil, err
	}
	project := gitObj(data["node"])
	if project["__typename"] != "ProjectV2" || project["id"] != id || !githubProjectURL(asString(project["url"])) {
		return nil, errors.New("GitHub Project not found")
	}
	if project["fields"] == nil {
		return nil, errors.New("GitHub Project fields unavailable")
	}
	return project, nil
}

func (db *Database) AttachPlanningBoard(project string, metadata map[string]any) (map[string]any, error) {
	id, url, title := asString(metadata["id"]), asString(metadata["url"]), asString(metadata["title"])
	if !validGraphID.MatchString(id) || !githubProjectURL(url) || title == "" || len(title) > 512 {
		return nil, errors.New("invalid GitHub Project metadata")
	}
	e := db.transaction(func() error {
		if e := requireProject(db, project); e != nil {
			return e
		}
		return db.execLocked(`INSERT INTO planning_boards(project,github_id,url,title,updated) VALUES(?,?,?,?,?)
            ON CONFLICT(project) DO UPDATE SET github_id=excluded.github_id,url=excluded.url,title=excluded.title,updated=excluded.updated`, project, id, url, title, stamp())
	})
	return map[string]any{"project": project, "github_id": id, "url": url, "title": title}, e
}
func (db *Database) PlanningBoard(project string) (map[string]any, error) {
	if e := requireID(project); e != nil {
		return nil, e
	}
	row, e := db.queryOne(`SELECT github_id,url,title,updated FROM planning_boards WHERE project=?`, project)
	if e != nil {
		return nil, e
	}
	if row == nil {
		return nil, errors.New("GitHub Project is not linked")
	}
	return row, nil
}
func (g *GitHub) ProjectAddDraft(projectID, title, body string) (map[string]any, error) {
	if !validGraphID.MatchString(projectID) || strings.TrimSpace(title) == "" || len(title) > 256 || len(body) > 20000 {
		return nil, errors.New("invalid planning draft")
	}
	data, e := g.GraphQL(`mutation($project: ID!, $title: String!, $body: String!) {
      addProjectV2DraftIssue(input: { projectId: $project, title: $title, body: $body }) { projectItem { id } }
    }`, map[string]any{"project": projectID, "title": title, "body": body})
	if e != nil {
		return nil, e
	}
	item := gitObj(gitObj(data["addProjectV2DraftIssue"])["projectItem"])
	if !validGraphID.MatchString(asString(item["id"])) {
		return nil, errors.New("GitHub returned invalid planning item")
	}
	return map[string]any{"item_id": item["id"], "project_id": projectID}, nil
}
func (g *GitHub) ProjectAddIssue(projectID, repo, kind string, number int) (map[string]any, error) {
	if !validGraphID.MatchString(projectID) || !validGitRepository(repo) || number < 1 || number > 100000000 || (kind != "issue" && kind != "pull_request") {
		return nil, errors.New("invalid GitHub planning item")
	}
	names := strings.SplitN(repo, "/", 2)
	data, e := g.GraphQL(`query($owner: String!, $repo: String!, $number: Int!) {
       repository(owner: $owner, name: $repo) { issue(number:$number) { id } pullRequest(number:$number) { id } }
    }`, map[string]any{"owner": names[0], "repo": names[1], "number": number})
	if e != nil {
		return nil, e
	}
	field := "issue"
	if kind == "pull_request" {
		field = "pullRequest"
	}
	nodeID := asString(gitObj(gitObj(data["repository"])[field])["id"])
	if !validGraphID.MatchString(nodeID) {
		return nil, errors.New("GitHub issue or pull request not found")
	}
	added, e := g.GraphQL(`mutation($project: ID!, $content: ID!) {
      addProjectV2ItemById(input:{projectId:$project, contentId:$content}) { item { id } }
    }`, map[string]any{"project": projectID, "content": nodeID})
	if e != nil {
		return nil, e
	}
	item := gitObj(gitObj(added["addProjectV2ItemById"])["item"])
	if !validGraphID.MatchString(asString(item["id"])) {
		return nil, errors.New("GitHub returned invalid planning item")
	}
	return map[string]any{"item_id": item["id"], "repository": repo, "number": number, "kind": kind}, nil
}
func (g *GitHub) ProjectSetSingleSelect(projectID, itemID, fieldID, optionID string) (map[string]any, error) {
	for _, v := range []string{projectID, itemID, fieldID, optionID} {
		if !validGraphID.MatchString(v) {
			return nil, errors.New("invalid GitHub Project field identifier")
		}
	}
	result, e := g.GraphQL(`mutation($project:ID!, $item:ID!, $field:ID!, $option:String!) {
       updateProjectV2ItemFieldValue(input:{ projectId:$project,itemId:$item,fieldId:$field,
           value:{ singleSelectOptionId:$option }}) { projectV2Item { id } }
    }`, map[string]any{"project": projectID, "item": itemID, "field": fieldID, "option": optionID})
	if e != nil {
		return nil, e
	}
	item := gitObj(gitObj(result["updateProjectV2ItemFieldValue"])["projectV2Item"])
	if item["id"] != itemID {
		return nil, errors.New("GitHub did not confirm field update")
	}
	return map[string]any{"item_id": itemID, "field_id": fieldID, "option_id": optionID}, nil
}

// ProjectByURL resolves the ordinary browser URL so users need not know GitHub
// GraphQL node IDs. The URL is parsed locally; it is never fetched directly.
func (g *GitHub) ProjectByURL(raw string) (map[string]any, error) {
	if !githubProjectURL(raw) {
		return nil, errors.New("invalid GitHub Project URL")
	}
	parsed, _ := url.Parse(raw)
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	number := 0
	if _, e := fmt.Sscanf(parts[3], "%d", &number); e != nil || number < 1 || number > 100000000 {
		return nil, errors.New("invalid GitHub Project number")
	}
	var query string
	if parts[0] == "orgs" {
		query = `query($login:String!,$number:Int!) { organization(login:$login) { projectV2(number:$number) { id title url readme shortDescription number } } }`
	} else {
		query = `query($login:String!,$number:Int!) { user(login:$login) { projectV2(number:$number) { id title url readme shortDescription number } } }`
	}
	data, e := g.GraphQL(query, map[string]any{"login": parts[1], "number": number})
	if e != nil {
		return nil, e
	}
	parent := "user"
	if parts[0] == "orgs" {
		parent = "organization"
	}
	project := gitObj(gitObj(data[parent])["projectV2"])
	if !validGraphID.MatchString(asString(project["id"])) || project["url"] != raw {
		return nil, errors.New("GitHub Project URL not accessible")
	}
	return project, nil
}
