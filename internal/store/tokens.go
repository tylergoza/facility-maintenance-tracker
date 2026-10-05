package store

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// apiTokenPrefix starts every API token, so one pasted somewhere it
// shouldn't be is easy to spot.
const apiTokenPrefix = "mt_"

// APIToken lets another app read from the API. The token itself is only
// known when it's made; afterwards it's Prefix, its first characters.
type APIToken struct {
	ID         int64
	Name       string // what uses it, e.g. "Event planner"
	Prefix     string
	CreatedBy  string
	CreatedAt  string
	LastUsedAt string // "" if never
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// CreateAPIToken makes a token for the app named name and returns it.
// This is the only time the token can be seen.
func (s *Store) CreateAPIToken(name string, userID int64) (string, error) {
	token := apiTokenPrefix + RandomToken()
	_, err := s.DB.Exec(`INSERT INTO api_tokens (name, token_hash, prefix, created_by) VALUES (?, ?, ?, ?)`,
		strings.TrimSpace(name), hashToken(token), token[:len(apiTokenPrefix)+6], nullInt(userID))
	return token, err
}

func (s *Store) ListAPITokens() ([]APIToken, error) {
	rows, err := s.DB.Query(`
		SELECT t.id, t.name, t.prefix, COALESCE(NULLIF(u.display_name, ''), u.username, ''), t.created_at, COALESCE(t.last_used_at, '')
		FROM api_tokens t LEFT JOIN users u ON u.id = t.created_by ORDER BY t.name COLLATE NOCASE, t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []APIToken
	for rows.Next() {
		var t APIToken
		if err := rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.CreatedBy, &t.CreatedAt, &t.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIToken(id int64) error {
	_, err := s.DB.Exec(`DELETE FROM api_tokens WHERE id = ?`, id)
	return err
}

// UseAPIToken finds the token and notes that it was used (at most once a
// minute, to spare the database a write on every request).
func (s *Store) UseAPIToken(token string) (*APIToken, error) {
	if !strings.HasPrefix(token, apiTokenPrefix) {
		return nil, ErrNotFound
	}
	var t APIToken
	h := hashToken(token)
	if err := s.DB.QueryRow(`SELECT id, name, prefix FROM api_tokens WHERE token_hash = ?`, h).Scan(&t.ID, &t.Name, &t.Prefix); err != nil {
		return nil, notFound(err)
	}
	_, err := s.DB.Exec(`UPDATE api_tokens SET last_used_at = datetime('now')
		WHERE id = ? AND (last_used_at IS NULL OR last_used_at < datetime('now', '-1 minute'))`, t.ID)
	return &t, err
}
