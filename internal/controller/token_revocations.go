package controller

import "errors"

// RevokeToken records a SHA-256 fingerprint, not an active bearer credential.
// A validated OIDC token with the recorded fingerprint is refused immediately.
func (db *Database) RevokeToken(digest string, expires int64) error {
	now := stamp()
	if !shaPattern.MatchString(digest) || float64(expires) <= now ||
		float64(expires) > now+86400 {
		return errors.New("invalid token fingerprint or expiration")
	}
	return db.transaction(func() error {
		if err := db.execLocked("DELETE FROM revoked_tokens WHERE expires<?", now); err != nil {
			return err
		}
		return db.execLocked("INSERT INTO revoked_tokens(digest,expires,created) VALUES(?,?,?) ON CONFLICT(digest) DO UPDATE SET expires=excluded.expires",
			digest, float64(expires), now)
	})
}

func (db *Database) TokenRevoked(digest string) bool {
	if !shaPattern.MatchString(digest) {
		return true
	}
	row, err := db.queryOne("SELECT digest FROM revoked_tokens WHERE digest=? AND expires>?", digest, stamp())
	return err != nil || row != nil
}

func (db *Database) TokenRevocations() ([]map[string]any, error) {
	return db.query("SELECT digest,expires,created FROM revoked_tokens WHERE expires>? ORDER BY created DESC LIMIT 1000", stamp())
}
