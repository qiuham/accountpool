package pool

import (
	"github.com/ham/accountpool/backend/internal/store"
	"strings"
)

// mailboxBlockState separates missing prerequisites from temporary service failures.
func mailboxBlockState(reason string) State {
	if strings.Contains(reason, "mailbox:address-not-found") {
		return MailboxMissing
	}
	if strings.Contains(reason, "mailbox:admin-auth-missing") ||
		strings.Contains(reason, "mailbox:address-mismatch") ||
		strings.Contains(reason, "MAIL_ADMIN_AUTH is required") ||
		strings.Contains(reason, "mailbox") && (strings.Contains(reason, "HTTP 400") || strings.Contains(reason, "HTTP 401") || strings.Contains(reason, "HTTP 403")) {
		return MailboxConfigError
	}
	return ""
}

func RetryableFailure(step, reason string, a *store.Account) bool {
	if reason == "mailbox-create-failed" || reason == "mailbox-prerequisite-failed" || mailboxBlockState(reason) != "" {
		return false
	}
	if a.RetiredAt != "" || a.State == string(Burned) || a.AccessToken != "" || a.RefreshToken != "" || step == "oauth" {
		return false
	}
	if reason == "personal-confirmation-required" || reason == "tool-permission-pending" || reason == "missing-chat-input" || reason == "" || strings.Contains(reason, "unknown") || strings.Contains(reason, "interrupted") || strings.Contains(reason, "reply-not-confirmed") || strings.Contains(reason, "send-failed") || strings.Contains(reason, "crashed") || reason == "no-artifacts" || reason == "bad-report" {
		return false
	}
	return true
}
