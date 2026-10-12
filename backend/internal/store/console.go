package store

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

func passwordKey(password, salt string) (string, error) {
	key, err := pbkdf2.Key(sha256.New, password, []byte(salt), 600000, 32)
	return hex.EncodeToString(key), err
}
func (s *Store) ConsoleReady() (bool, error) {
	var n int
	err := s.db.QueryRow("SELECT count(*) FROM console_admin").Scan(&n)
	return n > 0, err
}
func (s *Store) CreateConsoleAdmin(user, password string) error {
	if len(user) < 1 || len(user) > 80 || len(password) < 12 || len(password) > 256 {
		return errors.New("用户名需为 1–80 字符，密码需为 12–256 字符")
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	encoded := hex.EncodeToString(salt)
	hash, err := passwordKey(password, encoded)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT INTO console_admin(id,username,salt,password_hash) VALUES(1,?,?,?)", user, encoded, hash)
	return err
}
func (s *Store) CheckConsolePassword(user, password string) bool {
	if len(password) > 256 {
		return false
	}
	var actual, salt, hash string
	if s.db.QueryRow("SELECT username,salt,password_hash FROM console_admin WHERE id=1").Scan(&actual, &salt, &hash) != nil {
		return false
	}
	candidate, err := passwordKey(password, salt)
	return err == nil && subtle.ConstantTimeCompare([]byte(actual), []byte(user)) == 1 && subtle.ConstantTimeCompare([]byte(hash), []byte(candidate)) == 1
}
func sessionHash(token string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(token))) }
func (s *Store) CreateConsoleSession() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	token := hex.EncodeToString(raw)
	_, err := s.db.Exec("DELETE FROM console_sessions WHERE expires<=?", time.Now().Unix())
	if err != nil {
		return "", err
	}
	_, err = s.db.Exec("INSERT INTO console_sessions(token_hash,expires) VALUES(?,?)", sessionHash(token), time.Now().Add(24*time.Hour).Unix())
	return token, err
}
func (s *Store) ConsoleSession(token string) bool {
	if len(token) != 64 {
		return false
	}
	var expires int64
	err := s.db.QueryRow("SELECT expires FROM console_sessions WHERE token_hash=?", sessionHash(token)).Scan(&expires)
	return err == nil && expires > time.Now().Unix()
}
func (s *Store) DeleteConsoleSession(token string) error {
	_, err := s.db.Exec("DELETE FROM console_sessions WHERE token_hash=?", sessionHash(token))
	return err
}

func (s *Store) ResetConsolePassword(password string) error {
	if len(password) < 12 || len(password) > 256 {
		return errors.New("密码需为 12–256 字符")
	}
	salt := make([]byte, 32)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	encoded := hex.EncodeToString(salt)
	hash, err := passwordKey(password, encoded)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec("UPDATE console_admin SET salt=?,password_hash=? WHERE id=1", encoded, hash)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("管理员尚未初始化")
	}
	if _, err = tx.Exec("DELETE FROM console_sessions"); err != nil {
		return err
	}
	return tx.Commit()
}
