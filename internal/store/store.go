// Package store is the bot's SQLite database: which Telegram account is linked
// to which address, the secrets handed out for linking, and every rain. The
// chain holds the money; nothing here is needed to pay anyone, but a rain's id
// is issued here before it is sent, and the contract refuses an id twice.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct{ db *sql.DB }

const schema = `
CREATE TABLE IF NOT EXISTS users (
  tg_id     INTEGER PRIMARY KEY,
  name      TEXT NOT NULL,
  address   TEXT UNIQUE,       -- lower-case hex
  link_tx   TEXT,
  linked_at INTEGER
);
CREATE TABLE IF NOT EXISTS secrets (
  tg_id      INTEGER PRIMARY KEY,
  name       TEXT NOT NULL,
  secret     INTEGER NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  used_by    TEXT               -- the first address seen sending it
);
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS rains (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  chat_id    INTEGER NOT NULL,
  sender_tg  INTEGER NOT NULL,
  sender     TEXT NOT NULL,
  token      TEXT NOT NULL,     -- 0x000…0 for ZNN
  symbol     TEXT NOT NULL,
  each_units TEXT NOT NULL,     -- base units, decimal
  count      INTEGER NOT NULL,
  minutes    INTEGER NOT NULL,
  state      TEXT NOT NULL,     -- sending | paid | failed | lost
  tx         TEXT,
  note       TEXT,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS rain_recipients (
  rain_id INTEGER NOT NULL REFERENCES rains(id),
  tg_id   INTEGER NOT NULL,
  address TEXT NOT NULL,
  PRIMARY KEY (rain_id, address)
);
`

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

var (
	ErrNotFound    = errors.New("not found")
	ErrSecretTaken = errors.New("secret already issued")
)

// --- users ---

type User struct {
	TgID    int64
	Name    string
	Address string // lower-case hex; empty when not linked
}

func (s *Store) User(ctx context.Context, tgID int64) (*User, error) {
	var u User
	var addr sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT tg_id, name, address FROM users WHERE tg_id = ?`, tgID).Scan(&u.TgID, &u.Name, &addr)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	u.Address = addr.String
	return &u, err
}

// Addresses returns the linked address of each of ids that has one.
func (s *Store) Addresses(ctx context.Context, ids []int64) (map[int64]string, error) {
	out := map[int64]string{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.db.QueryContext(ctx, `SELECT tg_id, address FROM users WHERE address IS NOT NULL AND tg_id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var a string
		if err := rows.Scan(&id, &a); err != nil {
			return nil, err
		}
		out[id] = a
	}
	return out, rows.Err()
}

// Link binds an address to a user. The address moves if another user held it:
// the transaction that carried this user's secret proves whoever sent it
// controls the address now.
func (s *Store) Link(ctx context.Context, tgID int64, name, address, tx string, now time.Time) error {
	address = strings.ToLower(address)
	t, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer t.Rollback()
	if _, err := t.ExecContext(ctx, `UPDATE users SET address = NULL, link_tx = NULL, linked_at = NULL WHERE address = ? AND tg_id <> ?`, address, tgID); err != nil {
		return err
	}
	if _, err := t.ExecContext(ctx, `INSERT INTO users(tg_id, name, address, link_tx, linked_at) VALUES(?,?,?,?,?)
		ON CONFLICT(tg_id) DO UPDATE SET name = excluded.name, address = excluded.address, link_tx = excluded.link_tx, linked_at = excluded.linked_at`,
		tgID, name, address, tx, now.Unix()); err != nil {
		return err
	}
	return t.Commit()
}

// Unlink forgets a user's address.
func (s *Store) Unlink(ctx context.Context, tgID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE users SET address = NULL, link_tx = NULL, linked_at = NULL WHERE tg_id = ?`, tgID)
	return err
}

// --- secrets ---

// SetSecret stores a user's pending secret, replacing any earlier one. A
// secret already pending for somebody else is ErrSecretTaken, so the caller
// draws another.
func (s *Store) SetSecret(ctx context.Context, tgID int64, name string, secret int64, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO secrets(tg_id, name, secret, created_at) VALUES(?,?,?,?)
		ON CONFLICT(tg_id) DO UPDATE SET name = excluded.name, secret = excluded.secret, created_at = excluded.created_at, used_by = NULL`, tgID, name, secret, now.Unix())
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrSecretTaken
	}
	return err
}

// Secret returns the user's pending secret and when it was issued.
func (s *Store) Secret(ctx context.Context, tgID int64) (int64, time.Time, error) {
	var secret, at int64
	err := s.db.QueryRowContext(ctx, `SELECT secret, created_at FROM secrets WHERE tg_id = ?`, tgID).Scan(&secret, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, time.Time{}, ErrNotFound
	}
	return secret, time.Unix(at, 0), err
}

// Issued is a secret handed out: to whom, when, and the first address the
// link watcher saw send it, if any.
type Issued struct {
	TgID   int64
	Name   string
	At     time.Time
	UsedBy string
}

// SecretOwner looks a secret up.
func (s *Store) SecretOwner(ctx context.Context, secret int64) (*Issued, error) {
	var i Issued
	var at int64
	var used sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT tg_id, name, created_at, used_by FROM secrets WHERE secret = ?`, secret).Scan(&i.TgID, &i.Name, &at, &used)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	i.At, i.UsedBy = time.Unix(at, 0), used.String
	return &i, err
}

// UseSecret records the address that sent a user's secret.
func (s *Store) UseSecret(ctx context.Context, tgID int64, address string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE secrets SET used_by = ? WHERE tg_id = ?`, strings.ToLower(address), tgID)
	return err
}

// --- meta ---

func (s *Store) Meta(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

func (s *Store) SetMeta(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO meta(key, value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// --- rains ---

type Rain struct {
	ID       int64
	ChatID   int64
	SenderTg int64
	Sender   string
	Token    string
	Symbol   string
	Each     string
	Minutes  int
	To       []Recipient
}

type Recipient struct {
	TgID    int64
	Address string
}

// NewRain records a rain before it is sent and returns its id, which the
// contract then refuses to pay a second time.
func (s *Store) NewRain(ctx context.Context, r *Rain, now time.Time) (int64, error) {
	t, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer t.Rollback()
	res, err := t.ExecContext(ctx, `INSERT INTO rains(chat_id, sender_tg, sender, token, symbol, each_units, count, minutes, state, created_at)
		VALUES(?,?,?,?,?,?,?,?,'sending',?)`, r.ChatID, r.SenderTg, strings.ToLower(r.Sender), strings.ToLower(r.Token), r.Symbol, r.Each, len(r.To), r.Minutes, now.Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	for _, to := range r.To {
		if _, err := t.ExecContext(ctx, `INSERT INTO rain_recipients(rain_id, tg_id, address) VALUES(?,?,?)`, id, to.TgID, strings.ToLower(to.Address)); err != nil {
			return 0, err
		}
	}
	return id, t.Commit()
}

// FinishRain records how a rain ended: paid, failed (it cannot have landed),
// or lost (the bot cannot tell; see Recover).
func (s *Store) FinishRain(ctx context.Context, id int64, state, tx, note string) error {
	switch state {
	case "paid", "failed", "lost":
	default:
		return fmt.Errorf("rain %d: no state %q", id, state)
	}
	_, err := s.db.ExecContext(ctx, `UPDATE rains SET state = ?, tx = NULLIF(?, ''), note = NULLIF(?, '') WHERE id = ?`, state, tx, note, id)
	return err
}

// Sending lists rains that were sent and never finished: the bot stopped
// between sending and reading the receipt.
func (s *Store) Sending(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM rains WHERE state = 'sending' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
