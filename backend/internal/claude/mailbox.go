// Package claude implements mailbox, OAuth and account-health HTTP operations.
package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ham/accountpool/backend/internal/nettrace"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// MailClient 访问自建收信服务 configured mailbox service。
//
// 三个坑，都是实测踩出来的：
//  1. **必须直连，不走代理**——它跟 Claude 无关，走代理反而多一跳，还可能被 CF 挡
//  2. **UA 必须是浏览器**——mail 站挂在 CF 后面，按 UA 签名拉黑，
//     默认的 Go-http-client 一进去就吃 Error 1010
//  3. **建地址只传 local part**——传完整地址会被服务端剥掉 @ 和后面的部分
type MailClient struct {
	Trace     nettrace.Capture
	Base      string
	AdminAuth string
	Domain    string
	HTTP      *http.Client
}

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

func NewMailClient() *MailClient {
	base := os.Getenv("MAIL_BASE")
	auth := os.Getenv("MAIL_ADMIN_AUTH")
	dom := os.Getenv("MAIL_DOMAIN")
	return &MailClient{
		Base: base, AdminAuth: auth, Domain: dom,
		// 显式指定 Transport 且不设 Proxy，保证直连不受 http_proxy 影响
		HTTP: &http.Client{
			Timeout:   25 * time.Second,
			Transport: &http.Transport{Proxy: nil},
		},
	}
}

// CreatedAddress 是"建地址"的响应。
//
// **注意它跟列表响应的字段名不一样**（实测）：
//
//	建地址 → address_id / address / jwt / password
//	列表   → id / name
//
// 同一个服务两套命名，所以分成两个结构体，不要想着合并。
type CreatedAddress struct {
	JWT       string `json:"jwt"`
	Address   string `json:"address"`
	Password  string `json:"password"`
	AddressID int    `json:"address_id"`
}

// Address 是"列表"里的一项。只有这些字段，没有 jwt。
type Address struct {
	ID         int    `json:"id"`
	Name       string `json:"name"` // **完整地址**，不是 local part
	SourceMeta string `json:"source_meta"`
	CreatedAt  string `json:"created_at"`
	MailCount  int    `json:"mail_count"`
}

type Mail struct {
	CreatedAt string `json:"created_at"`
	ID        any    `json:"id"`
	Subject   string `json:"subject"`
	Text      string `json:"text"`
	Body      string `json:"body"`
	HTML      string `json:"html"`
	Raw       string `json:"raw"`
	Content   string `json:"content"`
}

func (c *MailClient) do(ctx context.Context, method, path string, headers map[string]string, body any) (int, []byte, error) {
	if auth, ok := headers["x-admin-auth"]; ok && auth == "" {
		return 0, nil, fmt.Errorf("MAIL_ADMIN_AUTH is required for mailbox administration")
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, rdr)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", browserUA)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	started := time.Now()
	var traceRaw []byte
	var traceErr error
	resp, err := c.HTTP.Do(req)
	defer func() {
		if c.Trace != nil {
			c.Trace(req, resp, traceRaw, traceErr, started)
		}
	}()
	traceErr = err

	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	traceRaw, traceErr = raw, readErr
	if readErr != nil {
		return resp.StatusCode, nil, readErr
	}
	return resp.StatusCode, raw, nil
}

func (c *MailClient) admin() map[string]string {
	return map[string]string{"x-admin-auth": c.AdminAuth}
}

// NewAddress 建一个收信地址。name 只传 local part（传完整地址服务端会剥掉 @ 和点号）。
func (c *MailClient) NewAddress(name string) (*CreatedAddress, error) {
	if c.Domain == "" {
		return nil, fmt.Errorf("MAIL_DOMAIN is required to create an address")
	}
	if strings.ContainsAny(name, "@.") {
		return nil, fmt.Errorf("name 只能是 local part，不能带 @ 或 .（收到 %q）", name)
	}
	code, raw, err := c.do(context.Background(), "POST", "/api/admin/new_address", c.admin(),
		map[string]string{"name": name, "domain": c.Domain})
	if err != nil {
		return nil, err
	}
	if code != 200 && code != 201 {
		return nil, fmt.Errorf("建地址失败 HTTP %d: %s", code, tail(string(raw), 200))
	}
	var wrap struct {
		Data *CreatedAddress `json:"data"`
	}
	if err := json.Unmarshal(raw, &wrap); err == nil && wrap.Data != nil {
		return wrap.Data, nil
	}
	var a CreatedAddress
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, fmt.Errorf("解析建地址响应: %w（原文 %s）", err, tail(string(raw), 200))
	}
	if a.Address == "" && a.AddressID == 0 {
		return nil, fmt.Errorf("建地址响应里没有 address/address_id：%s", tail(string(raw), 200))
	}
	return &a, nil
}

// ListAddresses 列已有地址。用于建号前查重，避免撞名。
func (c *MailClient) ListAddresses(limit, offset int) ([]Address, error) {
	code, raw, err := c.do(context.Background(), "GET",
		fmt.Sprintf("/api/admin/address?limit=%d&offset=%d", limit, offset), c.admin(), nil)
	if err != nil {
		return nil, err
	}
	if code != 200 {
		return nil, &MailboxError{"address-list-unavailable", code}
	}
	return parseAddrList(raw)
}

func parseAddrList(raw []byte) ([]Address, error) {
	var wrapped struct {
		Results []Address `json:"results"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&wrapped); err != nil || wrapped.Results == nil {
		return nil, fmt.Errorf("mailbox:invalid-address-list")
	}
	return wrapped.Results, nil
}

// DeleteAddress 删地址。**报废账号时先删邮箱再删本地**，
// 反过来的话中途失败会留下孤儿地址，下次建号撞名。
func (c *MailClient) DeleteAddress(id int) error {
	code, raw, err := c.do(context.Background(), "DELETE",
		fmt.Sprintf("/api/admin/delete_address/%d", id), c.admin(), nil)
	if err != nil {
		return err
	}
	if code != 200 && code != 204 {
		return fmt.Errorf("删地址失败 HTTP %d: %s", code, tail(string(raw), 200))
	}
	return nil
}

// FindAddressID 按邮箱地址找 id。
func (c *MailClient) FindAddressID(email string) (int, error) {
	// The mailbox API rejects page sizes greater than 100.
	const pageSize = 100
	for offset := 0; ; offset += pageSize {
		addrs, err := c.ListAddresses(pageSize, offset)
		if err != nil {
			return 0, err
		}
		for _, a := range addrs {
			if strings.EqualFold(a.Name, email) {
				return a.ID, nil
			}
		}
		if len(addrs) < pageSize {
			return 0, nil
		}
	}
}

// ListMails 拉收件箱。
func (c *MailClient) ListMails(ctx context.Context, jwt string, limit, offset int) ([]Mail, error) {
	code, raw, err := c.do(ctx, "GET",
		fmt.Sprintf("/api/parsed_mails?limit=%d&offset=%d", limit, offset),
		map[string]string{"Authorization": "Bearer " + jwt}, nil)
	if err != nil {
		return nil, err
	}
	if code == 401 {
		return nil, &MailboxError{"credential-invalid", code}
	}
	if code != 200 {
		return nil, &MailboxError{"inbox-unavailable", code}
	}
	var wrapped struct {
		Results []Mail `json:"results"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&wrapped); err != nil || wrapped.Results == nil {
		return nil, fmt.Errorf("mailbox:invalid-inbox-response")
	}
	return wrapped.Results, nil
}

var (
	// Claude 是 6 位数字；带关键词的优先，避免命中邮件里的其它数字
	codeKeywordRe = regexp.MustCompile(`(?i)(?:code|验证码|verification)[^\d]{0,24}(\d{6})`)
	codeBareRe    = regexp.MustCompile(`\b(\d{6})\b`)
	// 魔术链接。**token 在 fragment 里**（# 之后），fragment 不随请求发给服务器，
	// 只能由页面 JS 读取——所以必须在同一个浏览器 profile 内打开它。
	magicLinkRe = regexp.MustCompile(`https://claude\.ai/[^\s"'>]*magic[^\s"'>]*`)
)

// ExtractCode 从邮件正文里抠 6 位验证码。
func ExtractCode(mail Mail) string {
	body := strings.Join([]string{mail.Subject, mail.Text, mail.Body, mail.HTML, mail.Raw, mail.Content}, " ")
	if m := codeKeywordRe.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	if m := codeBareRe.FindStringSubmatch(body); m != nil {
		return m[1]
	}
	return ""
}

// ExtractMagicLink 从邮件正文里抠魔术链接。
//
// **这是默认路径。** 页面写的是「To continue, click the link sent to …」，
// 邮件主题是「Your secure link to Claude.ai is here」。页面上另有个
// 「Enter verification code」按钮走 6 位码，那是备选路径。
func ExtractMagicLink(mail Mail) string {
	body := strings.Join([]string{mail.Text, mail.Body, mail.HTML, mail.Raw, mail.Content}, " ")
	body = strings.ReplaceAll(body, "&amp;", "&")
	return magicLinkRe.FindString(body)
}

// WaitForMail 轮询等邮件。**这个轮询不是"调度冷却"**，是"等外部事件"的必需品——
// 邮件什么时候到不由我们决定，只能轮询。
//
// predicate 决定这封邮件算不算目标（比如只认主题带 "secure link" 的）。
func (c *MailClient) WaitForMail(jwt string, timeout, interval time.Duration,
	predicate func(Mail) bool) (Mail, error) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		mails, err := c.ListMails(context.Background(), jwt, 20, 0)
		if err != nil {
			var mailboxErr *MailboxError
			if errors.As(err, &mailboxErr) && mailboxErr.Status >= 400 && mailboxErr.Status < 500 {
				return Mail{}, err
			}
			lastErr = err
		} else {
			lastErr = nil
			for _, m := range mails {
				if predicate == nil || predicate(m) {
					return m, nil
				}
			}
		}
		if remaining := time.Until(deadline); remaining > 0 {
			time.Sleep(min(interval, remaining))
		}
	}
	if lastErr != nil {
		return Mail{}, fmt.Errorf("mailbox:read-failed: %w", lastErr)
	}
	return Mail{}, &MailboxError{"verification-timeout", 0}
}

func tail(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
