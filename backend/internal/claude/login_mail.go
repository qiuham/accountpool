package claude

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type LoginProof struct {
	Link string `json:"link,omitempty"`
	Code string `json:"code,omitempty"`
}

var loginURL = regexp.MustCompile(`https://claude\.ai/[^\s"'<>)]*`)

func mailID(mail Mail) (int64, error) {
	id, err := strconv.ParseInt(fmt.Sprint(mail.ID), 10, 64)
	if err != nil || id < 0 {
		return 0, &MailboxError{"invalid-response", 200}
	}
	return id, nil
}
func selectLoginProof(mails []Mail, watermark int64) (*LoginProof, error) {
	var best *LoginProof
	newest := watermark
	for _, mail := range mails {
		id, err := mailID(mail)
		if err != nil {
			return nil, err
		}
		if id <= newest {
			continue
		}
		body := html.UnescapeString(strings.Join([]string{mail.Subject, mail.Text, mail.Body, mail.HTML, mail.Content, mail.Raw}, " "))
		var proof *LoginProof
		for _, link := range loginURL.FindAllString(body, -1) {
			u, err := url.Parse(link)
			if err != nil || u.Host != "claude.ai" || u.User != nil {
				continue
			}
			path := strings.ToLower(u.Path)
			if strings.Contains(path, "magic") || strings.Contains(path, "verify") || strings.Contains(path, "auth") || strings.Contains(path, "login") {
				proof = &LoginProof{Link: link}
				break
			}
		}
		subject := strings.ToLower(mail.Subject)
		if proof == nil && strings.Contains(subject, "claude") && (strings.Contains(subject, "code") || strings.Contains(subject, "verification")) {
			if code := ExtractCode(mail); code != "" {
				proof = &LoginProof{Code: code}
			}
		}
		if proof != nil {
			best = proof
			newest = id
		}
	}
	return best, nil
}
func (c *MailClient) LoginWatermark(ctx context.Context, jwt string) (int64, error) {
	mails, err := c.ListMails(ctx, jwt, 100, 0)
	if err != nil {
		return 0, err
	}
	var max int64
	for _, mail := range mails {
		id, err := mailID(mail)
		if err != nil {
			return 0, err
		}
		if id > max {
			max = id
		}
	}
	return max, nil
}
func (c *MailClient) WaitForLogin(ctx context.Context, jwt string, watermark int64, timeout, interval time.Duration) (*LoginProof, error) {
	if timeout <= 0 || interval <= 0 {
		return nil, fmt.Errorf("invalid mailbox polling interval")
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var lastErr error
	for {
		mails, err := c.ListMails(ctx, jwt, 20, 0)
		if err == nil {
			lastErr = nil
			proof, err := selectLoginProof(mails, watermark)
			if err != nil {
				return nil, err
			}
			if proof != nil {
				return proof, nil
			}
		} else {
			if ctx.Err() != nil {
				if lastErr != nil {
					return nil, &MailboxError{"read-failed", 0}
				}
				return nil, &MailboxError{"verification-timeout", 0}
			}
			lastErr = err
			var me *MailboxError
			if errors.As(err, &me) && me.Status >= 400 && me.Status < 500 {
				return nil, err
			}
			if strings.Contains(err.Error(), "invalid-inbox-response") {
				return nil, &MailboxError{"invalid-response", 200}
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			if lastErr != nil {
				return nil, &MailboxError{"read-failed", 0}
			}
			return nil, &MailboxError{"verification-timeout", 0}
		case <-timer.C:
		}
	}
}
