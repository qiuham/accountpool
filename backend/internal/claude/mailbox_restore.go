package claude

import (
	"fmt"
	"strings"
)

// RestoreAddress is an explicit administrative operation, not an automatic retry.
// It never substitutes another address and verifies the returned mailbox identity.
func (c *MailClient) RestoreAddress(email string) (string, error) {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || strings.Contains(domain, "@") || c.Domain == "" || !strings.EqualFold(domain, c.Domain) {
		return "", fmt.Errorf("mailbox:unsupported-domain")
	}
	if c.AdminAuth == "" {
		return "", fmt.Errorf("mailbox:admin-auth-missing")
	}
	id, err := c.FindAddressID(email)
	if err != nil {
		return "", err
	}
	if id != 0 {
		return c.RecoverMailboxJWT(email)
	}
	created, err := c.NewAddress(local)
	if err != nil {
		return "", fmt.Errorf("mailbox:restore-request-failed")
	}
	if !strings.EqualFold(created.Address, email) || created.JWT == "" {
		return "", fmt.Errorf("mailbox:restored-address-mismatch")
	}
	if err = c.ValidateMailboxJWT(created.JWT, email); err != nil {
		return "", err
	}
	return created.JWT, nil
}
