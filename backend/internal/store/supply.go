package store

import (
	"encoding/json"
	"fmt"
	"net"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
)

type SupplySettings struct {
	SequenceDigits         int      `json:"sequenceDigits"`
	RegistrationGapMinutes int      `json:"registrationGapMinutes"`
	Target                 int      `json:"target"`
	Daily                  int      `json:"daily"`
	CheckHours             int      `json:"checkHours"`
	Prefix                 string   `json:"prefix"`
	MailBase               string   `json:"mailBase"`
	RegistrationDomain     string   `json:"registrationDomain"`
	Domains                []string `json:"domains"`
	MailPassword           string   `json:"mailPassword"`
}

func (v SupplySettings) Validate() error {
	if v.SequenceDigits < 0 || v.SequenceDigits > 12 || v.RegistrationGapMinutes < 0 || v.RegistrationGapMinutes > 1440 || v.Target < 0 || v.Target > 10000 || v.Daily < 1 || v.Daily > 1000 || v.CheckHours < 1 || v.CheckHours > 720 || !regexp.MustCompile(`^[a-zA-Z0-9_-]{0,48}$`).MatchString(v.Prefix) {
		return fmt.Errorf("账号池参数无效")
	}
	if v.MailBase != "" {
		u, e := url.Parse(v.MailBase)
		if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("邮箱服务地址无效")
		}
	}
	if len(v.Domains) > 100 {
		return fmt.Errorf("最多配置 100 个邮箱域名")
	}
	seen := map[string]bool{}
	for _, domain := range v.Domains {
		if !validMailboxDomain(domain) {
			return fmt.Errorf("邮箱域名格式无效，请填写小写域名，不含 @、协议或路径")
		}
		if seen[domain] {
			return fmt.Errorf("邮箱域名重复")
		}
		seen[domain] = true
	}
	if v.RegistrationDomain != "" && !seen[v.RegistrationDomain] {
		return fmt.Errorf("注册域名须从收件域名列表中选择")
	}
	if v.Target > 0 && (v.MailBase == "" || v.RegistrationDomain == "" || v.MailPassword == "") {
		return fmt.Errorf("启用自动注册前请配置邮箱服务、管理员密码和注册域名")
	}
	return nil
}
func validMailboxDomain(domain string) bool {
	if net.ParseIP(domain) != nil || len(domain) > 253 || domain != strings.ToLower(domain) || !strings.Contains(domain, ".") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) < 1 || len(label) > 63 || !regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`).MatchString(label) {
			return false
		}
	}
	return true
}

func (v SupplySettings) AcceptsMailbox(address string) bool {
	parsed, err := mail.ParseAddress(address)
	if err != nil || parsed.Address != address {
		return false
	}
	_, domain, ok := strings.Cut(address, "@")
	if !ok {
		return false
	}
	for _, allowed := range v.Domains {
		if strings.EqualFold(domain, allowed) {
			return true
		}
	}
	return false
}

func (s *Store) SupplySettings() (SupplySettings, error) {
	var raw string
	var v SupplySettings
	err := s.db.QueryRow("SELECT value FROM supply_settings WHERE id=1").Scan(&raw)
	if err != nil {
		return v, err
	}
	err = json.Unmarshal([]byte(raw), &v)
	if v.Domains == nil {
		v.Domains = []string{}
	}
	return v, err
}
func (s *Store) SaveSupplySettings(v SupplySettings) error {
	if err := v.Validate(); err != nil {
		return err
	}
	if v.Domains == nil {
		v.Domains = []string{}
	}
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var raw string
	if err = tx.QueryRow("SELECT value FROM supply_settings WHERE id=1").Scan(&raw); err != nil {
		return err
	}
	var previous SupplySettings
	if err = json.Unmarshal([]byte(raw), &previous); err != nil {
		return err
	}
	next := map[string]bool{}
	for _, d := range v.Domains {
		next[d] = true
	}
	for _, domain := range previous.Domains {
		if next[domain] {
			continue
		}
		var used bool
		err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM accounts a
   WHERE a.retired_at IS NULL AND a.state!='burned' AND lower(substr(COALESCE(a.email,''),instr(COALESCE(a.email,''),'@')+1))=?)`, domain).Scan(&used)
		if err != nil {
			return err
		}
		if used {
			return fmt.Errorf("域名 %s 仍被账号收件邮箱使用，请先处理相关账号", domain)
		}
	}
	if _, err = tx.Exec("UPDATE supply_settings SET value=? WHERE id=1", string(b)); err != nil {
		return err
	}
	return tx.Commit()
}
