package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var packageIdentifier = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9+._:=-]{0,100}$`)

func (db *Database) ImagePool() ([]map[string]any, error) {
	images, err := db.query(`SELECT p.id,p.image,p.architecture,p.description,p.created,
		COALESCE(s.status,'active') status,COALESCE(s.revision,1) revision
		FROM pool_images p LEFT JOIN pool_entry_states s ON s.kind='image' AND s.entry_id=p.id ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	nodes, err := db.query("SELECT id,capabilities FROM nodes WHERE last_seen>?", stamp()-90)
	if err != nil {
		return nil, err
	}
	for _, image := range images {
		available := []string{}
		if asString(image["status"]) != "active" {
			image["available_nodes"] = available
			continue
		}
		for _, node := range nodes {
			caps := decodeObject(asString(node["capabilities"]))
			if !workerHasImage(caps, asString(image["image"])) {
				continue
			}
			arch := normalizeArchitecture(asString(caps["arch"]))
			required := asString(image["architecture"])
			if required != "" && arch != required {
				continue
			}
			available = append(available, asString(node["id"]))
		}
		image["available_nodes"] = available
	}
	return images, nil
}

func normalizeArchitecture(arch string) string {
	switch arch {
	case "x86_64":
		return "amd64"
	case "aarch64":
		return "arm64"
	default:
		return arch
	}
}

func workerHasImage(caps map[string]any, name string) bool {
	images, ok := caps["images"].([]any)
	if !ok {
		return false
	}
	for _, x := range images {
		if asString(x) == name {
			return true
		}
	}
	return false
}

// RegisterPoolImage adds a controller-approved OCI reference to the shared catalog.
// Workers must separately allowlist it; registering a name cannot change worker policy.
func (db *Database) RegisterPoolImage(id, image, description, architecture string) (map[string]any, error) {
	if !validID.MatchString(id) || !validImage.MatchString(image) || len(description) > 256 ||
		(architecture != "" && architecture != "amd64" && architecture != "arm64") {
		return nil, errors.New("invalid image catalog entry")
	}
	err := db.exec("INSERT INTO pool_images(id,image,architecture,description,created) VALUES(?,?,?,?,?)",
		id, image, architecture, description, stamp())
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "image": image, "architecture": architecture}, nil
}

func (db *Database) SoftwarePool() ([]map[string]any, error) {
	rows, err := db.query(`SELECT p.id,p.image_id,p.packages,p.description,p.created,
		COALESCE(s.status,'active') status,COALESCE(s.revision,1) revision
		FROM pool_software p LEFT JOIN pool_entry_states s ON s.kind='software' AND s.entry_id=p.id ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		var packages []string
		if err := json.Unmarshal([]byte(asString(row["packages"])), &packages); err != nil {
			return nil, err
		}
		row["packages"] = packages
	}
	return rows, nil
}

// RegisterPoolSoftware retains immutable Debian/Ubuntu package selections.
// Package arguments are individually validated and never interpreted as shell input.
func (db *Database) RegisterPoolSoftware(id, imageID, description string, packages []string) (map[string]any, error) {
	if !validID.MatchString(id) || !validID.MatchString(imageID) || len(description) > 256 ||
		len(packages) == 0 || len(packages) > 64 {
		return nil, errors.New("invalid software catalog entry")
	}
	seen := map[string]bool{}
	for _, pkg := range packages {
		if !packageIdentifier.MatchString(pkg) || seen[pkg] {
			return nil, errors.New("invalid or duplicate package name")
		}
		seen[pkg] = true
	}
	row, err := db.queryOne("SELECT image FROM pool_images WHERE id=?", imageID)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, errors.New("catalog base image not found")
	}
	allowed, err := db.poolActive("image", imageID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errors.New("catalog base image is disabled")
	}
	image := asString(row["image"])
	// The selected package manager is only supported for known apt-based bases.
	// Other toolchains belong in prebuilt, operator-approved OCI images.
	if !strings.HasPrefix(image, "debian:") && !strings.HasPrefix(image, "ubuntu:") {
		return nil, errors.New("software package recipes require a Debian or Ubuntu base image")
	}
	encoded, err := json.Marshal(packages)
	if err != nil {
		return nil, err
	}
	if err := db.exec("INSERT INTO pool_software(id,image_id,packages,description,created) VALUES(?,?,?,?,?)",
		id, imageID, string(encoded), description, stamp()); err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "image_id": imageID, "packages": packages}, nil
}

// EnvironmentFromPool composes a standard versioned environment from
// operator-approved software entries. Installation is deferred until build.
func (db *Database) EnvironmentFromPool(project, name, imageID string, softwareIDs []string, expected int) (map[string]any, error) {
	if !validID.MatchString(imageID) || len(softwareIDs) > 32 {
		return nil, errors.New("invalid environment pool selection")
	}
	base, err := db.queryOne("SELECT image,architecture FROM pool_images WHERE id=?", imageID)
	if err != nil {
		return nil, err
	}
	if base == nil {
		return nil, errors.New("catalog base image not found")
	}
	allowed, err := db.poolActive("image", imageID)
	if err != nil {
		return nil, err
	}
	if !allowed {
		return nil, errors.New("catalog base image is disabled")
	}
	image := asString(base["image"])
	packages := []string{}
	seenSoftware := map[string]bool{}
	seenPackages := map[string]bool{}
	for _, id := range softwareIDs {
		if !validID.MatchString(id) || seenSoftware[id] {
			return nil, errors.New("invalid or duplicate software selection")
		}
		seenSoftware[id] = true
		row, err := db.queryOne("SELECT image_id,packages FROM pool_software WHERE id=?", id)
		if err != nil {
			return nil, err
		}
		if row == nil || asString(row["image_id"]) != imageID {
			return nil, fmt.Errorf("software %s is not approved for this image", id)
		}
		active, err := db.poolActive("software", id)
		if err != nil {
			return nil, err
		}
		if !active {
			return nil, fmt.Errorf("software %s is disabled", id)
		}
		var names []string
		if err := json.Unmarshal([]byte(asString(row["packages"])), &names); err != nil {
			return nil, err
		}
		for _, name := range names {
			if !packageIdentifier.MatchString(name) {
				return nil, errors.New("corrupt software package name")
			}
			if !seenPackages[name] {
				seenPackages[name] = true
				packages = append(packages, name)
			}
		}
	}
	sort.Strings(packages)
	dockerfile := ""
	if len(packages) > 0 {
		// The container build must have network access or a configured apt cache.
		dockerfile = "FROM " + image + "\nRUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends " +
			strings.Join(packages, " ") + " && rm -rf /var/lib/apt/lists/*\n"
	}
	result, err := db.EnvironmentDefine(project, name, image, dockerfile, "", asString(base["architecture"]), expected)
	if err != nil {
		return nil, err
	}
	result["image_pool_id"] = imageID
	result["software"] = softwareIDs
	return result, nil
}
