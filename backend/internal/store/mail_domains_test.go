package store

import (
	"path/filepath"
	"testing"
)

func domainSettings() SupplySettings {
	return SupplySettings{Daily: 3, CheckHours: 1, Domains: []string{"example.test", "second.test"}, RegistrationDomain: "example.test", MailBase: "https://mail.example.test", MailPassword: "fixture"}
}
func TestMailDomainsValidation(t *testing.T) {
	valid := domainSettings()
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, address := range []string{"person@example.test", "person@SECOND.test"} {
		if !valid.AcceptsMailbox(address) {
			t.Fatalf("rejected %s", address)
		}
	}
	for _, address := range []string{"person@hotmail.com", "person@fakeexample.test", "person@nested.example.test", "person@example.test.evil.test", "Person <person@example.test>", "person@example.test@evil.test"} {
		if valid.AcceptsMailbox(address) {
			t.Fatalf("accepted %s", address)
		}
	}
	for _, domains := range [][]string{{"example.test", "example.test"}, {"@example.test"}, {"https://example.test"}, {"example..test"}, {"-example.test"}, {"example-.test"}, {"EXAMPLE.test"}, {"example.test/path"}, {" example.test"}} {
		cfg := domainSettings()
		cfg.Domains = domains
		cfg.RegistrationDomain = ""
		if cfg.Validate() == nil {
			t.Fatalf("accepted invalid domains %v", domains)
		}
	}
	cfg := domainSettings()
	cfg.RegistrationDomain = "outside.test"
	if cfg.Validate() == nil {
		t.Fatal("unlisted registration domain accepted")
	}
	cfg = domainSettings()
	cfg.Target = 4
	cfg.RegistrationDomain = ""
	if cfg.Validate() == nil {
		t.Fatal("missing registration domain accepted for active supply")
	}
}
func TestMailDomainsPersistenceAndRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pool.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { db.Close() }()
	st := New(db)
	cfg := domainSettings()
	if err = st.SaveSupplySettings(cfg); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateAccount(&Account{Name: "native", Email: "native@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	bad := cfg
	bad.Domains = []string{"second.test"}
	bad.RegistrationDomain = "second.test"
	if st.SaveSupplySettings(bad) == nil {
		t.Fatal("removed domain used by native mailbox")
	}
	cfg.RegistrationDomain = "second.test"
	if err = st.SaveSupplySettings(cfg); err != nil {
		t.Fatal(err)
	}
	a, err := st.GetAccountByID(id)
	if err != nil || a.Email != "native@example.test" {
		t.Fatal("registration selection changed existing identity")
	}
	cfg.Domains = []string{"example.test"}
	cfg.RegistrationDomain = "example.test"
	if err = st.SaveSupplySettings(cfg); err != nil {
		t.Fatal(err)
	}
	db.Close()
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	st = New(db)
	saved, err := st.SupplySettings()
	if err != nil || len(saved.Domains) != 1 || saved.RegistrationDomain != "example.test" || saved.MailPassword != "fixture" {
		t.Fatal("configuration not persisted")
	}
}
