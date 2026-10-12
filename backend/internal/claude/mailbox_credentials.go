package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type MailboxError struct {
	Kind   string
	Status int
}

func (e *MailboxError) Error() string { return fmt.Sprintf("mailbox:%s (HTTP %d)", e.Kind, e.Status) }

// ValidateMailboxJWT checks identity without reading message contents.
func (c *MailClient) ValidateMailboxJWT(jwt, email string) error {
	code, raw, err := c.do(context.Background(), "GET", "/api/settings", map[string]string{"Authorization": "Bearer " + jwt}, nil)
	if err != nil {
		return fmt.Errorf("mailbox:network: %w", err)
	}
	if code == 401 {
		return &MailboxError{"credential-invalid", code}
	}
	if code != 200 {
		return &MailboxError{"settings-unavailable", code}
	}
	var settings struct {
		Address string `json:"address"`
	}
	if err := json.Unmarshal(raw, &settings); err != nil {
		return fmt.Errorf("mailbox:invalid-settings-response")
	}
	if !strings.EqualFold(settings.Address, email) || email == "" {
		return fmt.Errorf("mailbox:address-mismatch")
	}
	return nil
}

// RecoverMailboxJWT uses cloudflare_temp_email's existing administrator API.
// show_password returns a newly signed JWT, not a mailbox login password.
func (c *MailClient) RecoverMailboxJWT(email string) (string, error) {
	if c.AdminAuth == "" {
		return "", fmt.Errorf("mailbox:admin-auth-missing")
	}
	id, err := c.FindAddressID(email)
	if err != nil {
		return "", fmt.Errorf("mailbox:lookup: %w", err)
	}
	if id == 0 {
		return "", fmt.Errorf("mailbox:address-not-found")
	}
	code, raw, err := c.do(context.Background(), "GET", fmt.Sprintf("/api/admin/show_password/%d", id), c.admin(), nil)
	if err != nil {
		return "", fmt.Errorf("mailbox:recovery-network: %w", err)
	}
	if code != 200 {
		return "", &MailboxError{"recovery-failed", code}
	}
	var result struct {
		JWT string `json:"jwt"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || result.JWT == "" {
		return "", fmt.Errorf("mailbox:invalid-recovery-response")
	}
	if err := c.ValidateMailboxJWT(result.JWT, email); err != nil {
		return "", err
	}
	return result.JWT, nil
}
