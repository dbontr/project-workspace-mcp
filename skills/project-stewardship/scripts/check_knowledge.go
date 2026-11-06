package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var recordKinds = map[string]string{
	"decisions":   "decision",
	"findings":    "finding",
	"experiments": "experiment",
	"handoffs":    "handoff",
	"sources":     "source",
}

var requiredSections = map[string][]string{
	"decision":   {"Context", "Decision", "Alternatives", "Consequences", "Evidence"},
	"finding":    {"Claim", "Method", "Evidence", "Limitations"},
	"experiment": {"Question", "Reproduction", "Results", "Interpretation", "Artifacts"},
	"handoff":    {"Current state", "Verified", "Blockers", "Next actions"},
	"source":     {"Identity", "Locator", "Integrity", "Usage"},
}

var rootSections = map[string][]string{
	"Project.md": {"Purpose", "Scope", "Acceptance criteria", "Authorities"},
	"Context.md": {"Verified state", "In progress", "Blockers", "Next action", "References"},
}

var allowedStatuses = map[string]bool{
	"proposed":    true,
	"provisional": true,
	"verified":    true,
	"superseded":  true,
	"archived":    true,
}

var (
	identifier = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	datedName  = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-[a-z0-9]+(?:-[a-z0-9]+)*\.md$`)
	keyValue   = regexp.MustCompile(`^([a-z][a-z0-9_-]*):\s*(.*?)\s*$`)
)

func headingSections(body string) (map[string]string, bool) {
	sections := map[string]string{}
	current := ""
	title := false
	for _, line := range strings.Split(body, "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			current = strings.TrimSpace(line[3:])
			if _, exists := sections[current]; !exists {
				sections[current] = ""
			}
		case strings.HasPrefix(line, "# "):
			title = strings.TrimSpace(line[2:]) != ""
			current = ""
		case current != "":
			sections[current] += line + "\n"
		}
	}
	return sections, title
}

func validateSections(body string, expected []string, path string) []string {
	found, title := headingSections(body)
	problems := []string{}
	if !title {
		problems = append(problems, path+": missing document title")
	}
	for _, name := range expected {
		section, exists := found[name]
		switch {
		case !exists:
			problems = append(problems, fmt.Sprintf("%s: missing section '## %s'", path, name))
		case strings.TrimSpace(section) == "":
			problems = append(problems, fmt.Sprintf("%s: empty section '## %s'", path, name))
		}
	}
	return problems
}

func frontmatter(body, path string) (map[string]string, string, []string) {
	if !strings.HasPrefix(body, "---\n") {
		return nil, body, []string{path + ": required YAML frontmatter is missing"}
	}
	end := strings.Index(body[4:], "\n---\n")
	if end < 0 {
		return nil, body, []string{path + ": frontmatter closing separator is missing"}
	}
	end += 4
	meta := map[string]string{}
	problems := []string{}
	for _, line := range strings.Split(body[4:end], "\n") {
		match := keyValue.FindStringSubmatch(line)
		if match == nil {
			problems = append(problems, path+": frontmatter must use flat 'key: value' syntax")
			continue
		}
		key := match[1]
		if _, duplicate := meta[key]; duplicate {
			problems = append(problems, fmt.Sprintf("%s: duplicate frontmatter key '%s'", path, key))
		}
		meta[key] = strings.Trim(match[2], "\"'")
	}
	return meta, body[end+5:], problems
}

func validDate(value string) bool {
	parsed, err := time.Parse("2006-01-02", value)
	return err == nil && parsed.Format("2006-01-02") == value
}

func checkFile(path, kind string, seen map[string]string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{fmt.Sprintf("%s: cannot read Markdown: %v", path, err)}
	}
	body := string(data)
	meta, content, problems := frontmatter(body, path)
	for _, field := range []string{"id", "kind", "status", "date"} {
		if meta[field] == "" {
			problems = append(problems, fmt.Sprintf("%s: missing '%s'", path, field))
		}
	}
	id := meta["id"]
	if id != "" {
		if !identifier.MatchString(id) {
			problems = append(problems, path+": invalid stable record ID")
		}
		if prior, exists := seen[id]; exists {
			problems = append(problems, fmt.Sprintf("%s: duplicate record ID also used by %s", path, prior))
		} else {
			seen[id] = path
		}
	}
	if got := meta["kind"]; got != "" && got != kind {
		problems = append(problems, fmt.Sprintf("%s: kind must be '%s'", path, kind))
	}
	if status := meta["status"]; status != "" && !allowedStatuses[status] {
		problems = append(problems, path+": invalid record status")
	}
	for _, field := range []string{"date", "updated"} {
		if value := meta[field]; value != "" && !validDate(value) {
			problems = append(problems, fmt.Sprintf("%s: %s must be an ISO date", path, field))
		}
	}
	return append(problems, validateSections(content, requiredSections[kind], path)...)
}

func checkDirectory(root string, strict bool) []string {
	info, err := os.Stat(root)
	switch {
	case os.IsNotExist(err) && !strict:
		return nil
	case os.IsNotExist(err):
		return []string{root + ": knowledge directory does not exist"}
	case err != nil:
		return []string{fmt.Sprintf("%s: cannot inspect knowledge directory: %v", root, err)}
	case !info.IsDir():
		return []string{root + ": knowledge path is not a directory"}
	}
	problems := []string{}
	filenames := []string{"Project.md", "Context.md"}
	for _, filename := range filenames {
		path := filepath.Join(root, filename)
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) && !strict {
			continue
		}
		if os.IsNotExist(err) {
			problems = append(problems, path+": required canonical document is missing")
		} else if err != nil {
			problems = append(problems, fmt.Sprintf("%s: cannot read Markdown: %v", path, err))
		} else {
			problems = append(problems, validateSections(string(data), rootSections[filename], path)...)
		}
	}
	seen := map[string]string{}
	directories := []string{"decisions", "findings", "experiments", "handoffs", "sources"}
	for _, directory := range directories {
		folder := filepath.Join(root, directory)
		entries, err := os.ReadDir(folder)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: cannot inspect record path: %v", folder, err))
			continue
		}
		for _, entry := range entries {
			path := filepath.Join(folder, entry.Name())
			if !entry.Type().IsRegular() || filepath.Ext(path) != ".md" {
				problems = append(problems, fmt.Sprintf("%s: records must be direct Markdown children of %s/", path, directory))
				continue
			}
			if directory != "sources" && !datedName.MatchString(entry.Name()) {
				problems = append(problems, path+": record filename must be YYYY-MM-DD-short-slug.md")
			}
			problems = append(problems, checkFile(path, recordKinds[directory], seen)...)
		}
	}
	sort.Strings(problems)
	return problems
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("check_knowledge", flag.ContinueOnError)
	flags.SetOutput(stderr)
	strict := flags.Bool("strict", false, "require Project.md and Context.md")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: check_knowledge [--strict] DIRECTORY")
		return 2
	}
	problems := checkDirectory(flags.Arg(0), *strict)
	for _, problem := range problems {
		fmt.Fprintln(stderr, "ERROR:", problem)
	}
	if len(problems) > 0 {
		fmt.Fprintf(stderr, "Knowledge structure failed: %d issue(s).\n", len(problems))
		return 1
	}
	fmt.Fprintln(stdout, "Knowledge structure: OK (structural checks only; evidence not verified).")
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
