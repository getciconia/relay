package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"
)

// Linking Home Assistant, as in the cloud: Home Assistant asks for a code,
// the user approves it in the panel, Home Assistant polls and gets a token.
const (
	LinkCodeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789" // no 0/O/1/I/L
	LinkCodeLength   = 8
	LinkLifetime     = 10 * time.Minute
	LinkPollInterval = 5 // seconds, a hint for clients
	clientNameMax    = 60
)

// LinkRequest is one pending approval.
type LinkRequest struct {
	ID         int64
	UserCode   string
	ClientName string
	Status     string
	ExpiresAt  time.Time
}

// DisplayCode is the code as shown: "K7QX-49MP".
func (l LinkRequest) DisplayCode() string { return l.UserCode[:4] + "-" + l.UserCode[4:] }

// Expired says whether the code ran out.
func (l LinkRequest) Expired(now time.Time) bool { return !now.Before(l.ExpiresAt) }

var nonCode = regexp.MustCompile(`[^A-Z0-9]`)

// NormalizeCode upper-cases a typed code and drops dashes and spaces.
func NormalizeCode(code string) string { return nonCode.ReplaceAllString(strings.ToUpper(code), "") }

// IssueLink creates a link request; the device code goes to Home Assistant only.
func (s *Store) IssueLink(ctx context.Context, clientName string) (LinkRequest, string, error) {
	now := s.now()
	s.db.ExecContext(ctx, `DELETE FROM link_requests WHERE expires_at < ?`, ms(now.Add(-24*time.Hour)))
	clientName = strings.Join(strings.Fields(clientName), " ")
	if r := []rune(clientName); len(r) > clientNameMax {
		clientName = string(r[:clientNameMax])
	}
	if clientName == "" {
		clientName = "Home Assistant"
	}
	deviceCode := RandomToken("", 32)
	for range 10 {
		code := randomCode()
		res, err := s.db.ExecContext(ctx, `INSERT INTO link_requests (user_code, device_code_digest, client_name, expires_at) VALUES (?, ?, ?, ?) ON CONFLICT DO NOTHING`,
			code, Digest(deviceCode), clientName, ms(now.Add(LinkLifetime)))
		if err != nil {
			return LinkRequest{}, "", err
		}
		if n, _ := res.RowsAffected(); n == 1 {
			id, _ := res.LastInsertId()
			return LinkRequest{ID: id, UserCode: code, ClientName: clientName, Status: "pending", ExpiresAt: now.Add(LinkLifetime)}, deviceCode, nil
		}
	}
	return LinkRequest{}, "", errors.New("store: no free link code")
}

func randomCode() string {
	var b strings.Builder
	for range LinkCodeLength {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(LinkCodeAlphabet))))
		if err != nil {
			panic(err)
		}
		b.WriteByte(LinkCodeAlphabet[n.Int64()])
	}
	return b.String()
}

func (s *Store) link(ctx context.Context, where string, args ...any) (LinkRequest, error) {
	var l LinkRequest
	var expires int64
	err := s.db.QueryRowContext(ctx, `SELECT id, user_code, client_name, status, expires_at FROM link_requests WHERE `+where, args...).
		Scan(&l.ID, &l.UserCode, &l.ClientName, &l.Status, &expires)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	l.ExpiresAt = fromMS(expires)
	return l, err
}

// PendingLink finds a live, pending request by its typed code.
func (s *Store) PendingLink(ctx context.Context, code string) (LinkRequest, error) {
	code = NormalizeCode(code)
	if len(code) != LinkCodeLength {
		return LinkRequest{}, ErrNotFound
	}
	return s.link(ctx, `user_code = ? AND status = 'pending' AND expires_at > ?`, code, ms(s.now()))
}

// PendingLinks lists live requests waiting for approval, newest first.
func (s *Store) PendingLinks(ctx context.Context) ([]LinkRequest, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, user_code, client_name, status, expires_at FROM link_requests
		WHERE status = 'pending' AND expires_at > ? ORDER BY id DESC LIMIT 5`, ms(s.now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkRequest
	for rows.Next() {
		var l LinkRequest
		var expires int64
		if err := rows.Scan(&l.ID, &l.UserCode, &l.ClientName, &l.Status, &expires); err != nil {
			return nil, err
		}
		l.ExpiresAt = fromMS(expires)
		out = append(out, l)
	}
	return out, rows.Err()
}

// DecideLink approves or denies a pending request.
func (s *Store) DecideLink(ctx context.Context, id int64, approve bool) error {
	status := "denied"
	if approve {
		status = "approved"
	}
	res, err := s.db.ExecContext(ctx, `UPDATE link_requests SET status = ?, approved_at = ? WHERE id = ? AND status = 'pending' AND expires_at > ?`,
		status, ms(s.now()), id, ms(s.now()))
	return affected(res, err)
}

// PollResult is what Home Assistant gets when it polls.
type PollResult struct {
	Status string // pending, approved, denied, expired
	Token  string // only when approved, once
}

// PollLink is Home Assistant asking whether its code was approved. An
// approved request turns into a connection and its token, once.
func (s *Store) PollLink(ctx context.Context, deviceCode string) (PollResult, error) {
	if deviceCode == "" {
		return PollResult{}, ErrNotFound
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PollResult{}, err
	}
	defer tx.Rollback()
	var id, expires int64
	var status, clientName string
	err = tx.QueryRowContext(ctx, `SELECT id, status, expires_at, client_name FROM link_requests WHERE device_code_digest = ?`, Digest(deviceCode)).
		Scan(&id, &status, &expires, &clientName)
	if errors.Is(err, sql.ErrNoRows) {
		return PollResult{}, ErrNotFound
	}
	if err != nil {
		return PollResult{}, err
	}
	switch {
	case status == "approved":
		token := RandomToken("cir_", 32)
		if _, err := tx.ExecContext(ctx, `UPDATE link_requests SET status = 'consumed' WHERE id = ?`, id); err != nil {
			return PollResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO connections (name, token_digest, created_at) VALUES (?, ?, ?)`, clientName, Digest(token), ms(s.now())); err != nil {
			return PollResult{}, err
		}
		return PollResult{Status: "approved", Token: token}, tx.Commit()
	case status == "denied":
		return PollResult{Status: "denied"}, nil
	case status == "consumed" || expires <= ms(s.now()):
		return PollResult{Status: "expired"}, nil
	}
	return PollResult{Status: "pending"}, nil
}

// Connection is one linked Home Assistant.
type Connection struct {
	ID         int64
	Name       string
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

// Connections lists linked Home Assistants, newest first.
func (s *Store) Connections(ctx context.Context) ([]Connection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at, last_used_at FROM connections ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Connection
	for rows.Next() {
		var c Connection
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&c.ID, &c.Name, &created, &used); err != nil {
			return nil, err
		}
		c.CreatedAt, c.LastUsedAt = fromMS(created), nullTime(used)
		out = append(out, c)
	}
	return out, rows.Err()
}

// Authenticate finds the connection a token belongs to and notes its use
// (at most once a minute).
func (s *Store) Authenticate(ctx context.Context, token string) (bool, error) {
	if !strings.HasPrefix(token, "cir_") {
		return false, nil
	}
	now := ms(s.now())
	res, err := s.db.ExecContext(ctx, `UPDATE connections SET last_used_at = ? WHERE token_digest = ? AND (last_used_at IS NULL OR last_used_at < ?)`,
		now, Digest(token), now-60_000)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil
	}
	var id int64
	err = s.db.QueryRowContext(ctx, `SELECT id FROM connections WHERE token_digest = ?`, Digest(token)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// DeleteConnection disconnects a Home Assistant; its token stops working.
func (s *Store) DeleteConnection(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM connections WHERE id = ?`, id)
	return affected(res, err)
}
