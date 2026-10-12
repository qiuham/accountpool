package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// ParseProxyURL validates a complete URL without including credentials in errors.
func ParseProxyURL(raw string) (*url.URL, int, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, 0, fmt.Errorf("invalid proxy URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "socks5" {
		return nil, 0, fmt.Errorf("unsupported proxy protocol")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, 0, fmt.Errorf("proxy requires host and explicit port, without query or path")
	}
	return u, port, nil
}

// BindAccountProxy is idempotent; an existing account cannot silently change exit.
func (s *Store) BindAccountProxy(accountID, proxyID int64) error {
	_, err := s.db.Exec(`INSERT INTO account_proxies(account_id,proxy_id) VALUES (?,?) ON CONFLICT(account_id) DO NOTHING`, accountID, proxyID)
	if err != nil {
		return err
	}
	var actual int64
	if err := s.db.QueryRow(`SELECT proxy_id FROM account_proxies WHERE account_id=?`, accountID).Scan(&actual); err != nil {
		return err
	}
	if actual != proxyID {
		return fmt.Errorf("account already bound to another proxy")
	}
	return nil
}

func (s *Store) AccountProxy(accountID int64) (*Proxy, error) {
	var p Proxy
	err := s.db.QueryRow(`SELECT p.id,p.url,p.state,COALESCE(p.cooldown_until,'') FROM account_proxies b JOIN proxies p ON p.id=b.proxy_id WHERE b.account_id=?`, accountID).Scan(&p.ID, &p.URL, &p.State, &p.CooldownUntil)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) EnsureProxy(raw string) (int64, error) {
	if _, _, err := ParseProxyURL(raw); err != nil {
		return 0, err
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM proxies WHERE url=?`, raw).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	return s.AddProxy(Proxy{URL: raw})
}
