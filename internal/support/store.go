package support

import (
	"database/sql"
	"encoding/json"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"time"
)

type Message struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	Text      string    `json:"text"`
	Response  *Response `json:"response,omitempty"`
	CreatedAt string    `json:"createdAt"`
}
type Store struct{ DB *sql.DB }

func OpenStore(path string) (*Store, error) {
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return nil, e
	}
	db, e := sql.Open("sqlite", path)
	if e != nil {
		return nil, e
	}
	db.SetMaxOpenConns(1)
	_, e = db.Exec(`PRAGMA journal_mode=WAL; PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS sessions(id TEXT PRIMARY KEY,user_id TEXT NOT NULL,expires INTEGER NOT NULL);
CREATE TABLE IF NOT EXISTS messages(id INTEGER PRIMARY KEY AUTOINCREMENT,user_id TEXT NOT NULL,ticket_id TEXT NOT NULL,role TEXT NOT NULL,text TEXT NOT NULL,response TEXT NOT NULL,created_at TEXT NOT NULL);
CREATE INDEX IF NOT EXISTS thread ON messages(user_id,ticket_id,id);`)
	if e != nil {
		db.Close()
		return nil, e
	}
	return &Store{db}, nil
}
func (s *Store) Session(id, user string) error {
	_, e := s.DB.Exec(`INSERT INTO sessions VALUES(?,?,?)`, id, user, time.Now().Add(24*time.Hour).Unix())
	return e
}
func (s *Store) Actor(id string) (string, error) {
	var u string
	e := s.DB.QueryRow(`SELECT user_id FROM sessions WHERE id=? AND expires>?`, id, time.Now().Unix()).Scan(&u)
	return u, e
}
func (s *Store) History(user, ticket string) ([]Message, error) {
	rows, e := s.DB.Query(`SELECT id,role,text,response,created_at FROM (SELECT * FROM messages WHERE user_id=? AND ticket_id=? ORDER BY id DESC LIMIT 40) ORDER BY id`, user, ticket)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []Message{}
	for rows.Next() {
		var m Message
		var raw string
		if e = rows.Scan(&m.ID, &m.Role, &m.Text, &raw, &m.CreatedAt); e != nil {
			return nil, e
		}
		if raw != "" {
			var r Response
			if json.Unmarshal([]byte(raw), &r) == nil {
				m.Response = &r
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) SaveTurn(user, ticket, question string, r Response) error {
	tx, e := s.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, e = tx.Exec(`INSERT INTO messages(user_id,ticket_id,role,text,response,created_at) VALUES(?,?,?,?,?,?)`, user, ticket, "user", question, "", now); e != nil {
		return e
	}
	b, _ := json.Marshal(r)
	if _, e = tx.Exec(`INSERT INTO messages(user_id,ticket_id,role,text,response,created_at) VALUES(?,?,?,?,?,?)`, user, ticket, "assistant", r.Text, string(b), now); e != nil {
		return e
	}
	return tx.Commit()
}
func (s *Store) Reset(user, ticket string) error {
	_, e := s.DB.Exec(`DELETE FROM messages WHERE user_id=? AND ticket_id=?`, user, ticket)
	return e
}
