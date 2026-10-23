package store

import (
	"database/sql"
	"time"
)

func timeNow() int64 { return time.Now().Unix() }

// ---- settings helpers ----

func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.DB.QueryRow(`SELECT value FROM settings WHERE key=?`, key).Scan(&v)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return v, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.DB.Exec(`INSERT INTO settings (key,value) VALUES (?,?)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// ---- user helpers ----

func (s *Store) UserByOpenid(openid string) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT id,openid,session_key,nickname,avatar,mobile,status,created_at,last_login_at
		FROM users WHERE openid=?`, openid))
}

func (s *Store) UserByID(id int64) (*User, error) {
	return scanUser(s.DB.QueryRow(`SELECT id,openid,session_key,nickname,avatar,mobile,status,created_at,last_login_at
		FROM users WHERE id=?`, id))
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	err := row.Scan(&u.ID, &u.Openid, &u.SessionKey, &u.Nickname, &u.Avatar,
		&u.Mobile, &u.Status, &u.CreatedAt, &u.LastLoginAt)
	if err != nil {
		return nil, err
	}
	return &u, nil
}

type User struct {
	ID          int64
	Openid      string
	SessionKey  string
	Nickname    string
	Avatar      string
	Mobile      string
	Status      int
	CreatedAt   int64
	LastLoginAt int64
}
