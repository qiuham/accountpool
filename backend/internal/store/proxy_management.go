package store

import (
	"fmt"
	"strings"
)

// ImportProxies validates the whole batch before writing; duplicate URLs are ignored.
func (s *Store) ImportProxies(urls []string) error {
	if len(urls) == 0 || len(urls) > 100 {
		return fmt.Errorf("每次请输入 1–100 个代理")
	}
	normalized := make([]string, 0, len(urls))
	for i, raw := range urls {
		u, _, err := ParseProxyURL(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("第 %d 行代理地址无效", i+1)
		}
		u.Path = ""
		normalized = append(normalized, u.String())
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, raw := range normalized {
		if _, err = tx.Exec("INSERT INTO proxies(url,state) SELECT ?,'available' WHERE NOT EXISTS(SELECT 1 FROM proxies WHERE url=?)", raw, raw); err != nil {
			return fmt.Errorf("保存代理失败")
		}
	}
	return tx.Commit()
}

// ChangeAccountProxy is explicit, and only allowed while account maintenance is paused.
func (s *Store) ChangeAccountProxy(accountID, proxyID int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var allowed bool
	err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM accounts a WHERE a.id=? AND a.retired_at IS NULL AND a.state!='burned'
 AND EXISTS(SELECT 1 FROM account_maintenance_pauses WHERE account_id=a.id)
 AND NOT EXISTS(SELECT 1 FROM jobs WHERE account_id=a.id AND state IN ('pending','running')))`, accountID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("请先暂停该账号维护，并等待或取消在途任务")
	}
	if proxyID == 0 {
		if _, err = tx.Exec("DELETE FROM account_proxies WHERE account_id=?", accountID); err != nil {
			return err
		}
		if _, err = tx.Exec("UPDATE jobs SET proxy_id=NULL WHERE account_id=? AND state='pending'", accountID); err != nil {
			return err
		}
		if _, err = tx.Exec("INSERT INTO events(account_id,at,kind,detail) VALUES(?,?,?,?)", accountID, now(), "proxy-changed", "已清除代理绑定，使用直连"); err != nil {
			return err
		}
		return tx.Commit()
	}
	if proxyID < 0 {
		return fmt.Errorf("请选择代理")
	}
	err = tx.QueryRow("SELECT EXISTS(SELECT 1 FROM proxies WHERE id=? AND state='available')", proxyID).Scan(&allowed)
	if err != nil {
		return err
	}
	if !allowed {
		return fmt.Errorf("代理不存在或不可用")
	}
	_, err = tx.Exec("INSERT INTO account_proxies(account_id,proxy_id) VALUES(?,?) ON CONFLICT(account_id) DO UPDATE SET proxy_id=excluded.proxy_id", accountID, proxyID)
	if err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO events(account_id,at,kind,detail) VALUES(?,?,?,?)", accountID, now(), "proxy-changed", fmt.Sprintf("explicit proxy binding: %d", proxyID))
	if err != nil {
		return err
	}
	return tx.Commit()
}
