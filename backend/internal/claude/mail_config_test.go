package claude

import "testing"

func TestMailConfigurationFromEnvironment(t *testing.T) {
	t.Setenv("MAIL_BASE", "https://mail.example.test")
	t.Setenv("MAIL_DOMAIN", "example.test")
	t.Setenv("MAIL_ADMIN_AUTH", "fixture-admin-secret")
	c := NewMailClient()
	if c.Base != "https://mail.example.test" || c.Domain != "example.test" || c.AdminAuth != "fixture-admin-secret" {
		t.Fatal("mail environment not applied")
	}
	t.Setenv("MAIL_ADMIN_AUTH", "changed-fixture")
	if NewMailClient().AdminAuth != "changed-fixture" {
		t.Fatal("admin configuration was hardcoded or cached")
	}
}
