package controller

import (
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
)

var externalName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// External resources are operator records, not scheduler workers. They do not
// grant remote execution rights or expose credentials to ordinary workspaces.
func (db *Database) RegisterExternalResource(id, kind, adapter, endpoint, operator, policy string) (map[string]any, error) {
	if !validID.MatchString(id) || !externalName.MatchString(operator) ||
		(len(policy) > 512) || strings.TrimSpace(policy) == "" {
		return nil, errors.New("invalid external resource registration")
	}
	switch kind {
	case "machine", "cluster", "eda-guest", "board":
	default:
		return nil, errors.New("unsupported external resource kind")
	}
	switch adapter {
	case "ssh", "slurm", "kubernetes":
	default:
		return nil, errors.New("unsupported external resource adapter")
	}
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" || strings.ContainsAny(host, "/@\\\x00\r\n") ||
		strings.Contains(host, "..") || len(host) > 253 {
		return nil, errors.New("external endpoint requires a host and port without credentials")
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return nil, errors.New("invalid external endpoint port")
	}
	now := stamp()
	err = db.exec(`INSERT INTO external_resources
		(id,kind,adapter,endpoint,operator,policy,status,created,updated)
		VALUES(?,?,?,?,?,?,'registered',?,?)`,
		id, kind, adapter, endpoint, operator, policy, now, now)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": id, "kind": kind, "adapter": adapter,
		"endpoint": endpoint, "operator": operator, "policy": policy,
		"status": "registered", "connected": false, "free_pool": false}, nil
}

func (db *Database) ExternalResources() ([]map[string]any, error) {
	return db.query(`SELECT id,kind,adapter,endpoint,operator,policy,status,created,updated
		FROM external_resources ORDER BY id`)
}

func (db *Database) ExternalResourceLeases(id string) ([]map[string]any, error) {
	if id != "" && !validID.MatchString(id) {
		return nil, errors.New("invalid resource ID")
	}
	return db.query(`SELECT id,resource,scope,scope_id,principal,expires,released,created
		FROM external_resource_leases
		WHERE (?='' OR resource=?) ORDER BY created DESC LIMIT 200`, id, id)
}

// ReserveExternalResource records a local request only. An external adapter
// must separately confirm site scheduling and access before execution.
func (db *Database) ReserveExternalResource(resource, scope, scopeID, principal string, minutes int, confirm bool) (map[string]any, error) {
	if !confirm || !validID.MatchString(resource) || minutes < 1 || minutes > 1440 ||
		(scope != "task" && scope != "project") {
		return nil, errors.New("explicit resource, scope, duration and confirmation required")
	}
	if principal != "controller-owner" && !validPrincipal(principal) {
		return nil, errors.New("invalid lease principal")
	}
	var result map[string]any
	err := db.transaction(func() error {
		if scope == "project" {
			if err := requireProject(db, scopeID); err != nil {
				return err
			}
		} else {
			row, err := db.lockedOne("SELECT id FROM personal_tasks WHERE id=? AND owner=? AND status='active'", scopeID, principal)
			if err != nil {
				return err
			}
			if row == nil {
				return errors.New("active task owned by caller required")
			}
		}
		entry, err := db.lockedOne("SELECT id,adapter,status FROM external_resources WHERE id=?", resource)
		if err != nil {
			return err
		}
		if entry == nil || asString(entry["status"]) != "registered" {
			return errors.New("external resource not registered")
		}
		// Expiration never proves the external scheduler released its allocation.
		// Explicit reconciliation is required before the resource can be reused.
		now := stamp()
		current, err := db.lockedOne("SELECT id FROM external_resource_leases WHERE resource=? AND released IS NULL", resource)
		if err != nil {
			return err
		}
		if current != nil {
			return errors.New("external resource reservation conflict")
		}
		id := randomHex(16)
		expiry := now + float64(minutes*60)
		if err := db.execLocked("INSERT INTO external_resource_leases VALUES(?,?,?,?,?,?,NULL,?)",
			id, resource, scope, scopeID, principal, expiry, now); err != nil {
			return err
		}
		result = map[string]any{"id": id, "resource": resource, "scope": scope, "scope_id": scopeID,
			"expires": expiry, "status": "pending_external_adapter", "remote_lease_confirmed": false}
		return nil
	})
	return result, err
}

func (db *Database) ReleaseExternalResource(id string, confirm bool) (map[string]any, error) {
	if !confirm || !contributionArtifactID.MatchString(id) {
		return nil, errors.New("explicit lease ID and confirmation required")
	}
	var record map[string]any
	err := db.transaction(func() error {
		row, err := db.lockedOne("SELECT resource,released FROM external_resource_leases WHERE id=?", id)
		if err != nil || row == nil || row["released"] != nil {
			return errors.New("active external reservation not found")
		}
		if err := db.execLocked("UPDATE external_resource_leases SET released=? WHERE id=? AND released IS NULL", stamp(), id); err != nil {
			return err
		}
		record = map[string]any{"id": id, "resource": row["resource"], "status": "released"}
		return nil
	})
	return record, err
}
