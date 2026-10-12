package main

import (
	"github.com/ham/accountpool/backend/internal/pool"
	"github.com/ham/accountpool/backend/internal/runner"
	"github.com/ham/accountpool/backend/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimePolicyReload(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	st := store.New(db)
	s := &pool.Scheduler{}
	r := &runner.Runner{}
	load := runtimePolicy(st, s, r)
	if err := load(); err != nil {
		t.Fatal(err)
	}
	if s.MinGap != time.Minute {
		t.Fatal(s.MinGap)
	}
	v := store.RuntimeSettings{GapSeconds: 120, CooldownSeconds: 3600, FailureLimit: 4, TaskTimeoutSeconds: 600}
	if err := st.SaveRuntimeSettings(v); err != nil {
		t.Fatal(err)
	}
	if err := load(); err != nil {
		t.Fatal(err)
	}
	if s.MinGap != 2*time.Minute || s.BaseCooldown != time.Hour || s.ConsecutiveFailMax != 4 || r.Timeout != 10*time.Minute || r.Cooldown != s.MinGap {
		t.Fatal("policy not applied")
	}
	cfg, err := st.SupplySettings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Domains = []string{"first.test", "second.test"}
	cfg.RegistrationDomain = "second.test"
	if err = st.SaveSupplySettings(cfg); err != nil {
		t.Fatal(err)
	}
	if err = load(); err != nil {
		t.Fatal(err)
	}
	if r.Mail.Domain != "second.test" {
		t.Fatal("registration domain not applied")
	}

}
