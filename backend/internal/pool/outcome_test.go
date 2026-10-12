package pool

import (
	"testing"
)

func TestOutcomeCredentialsBelongToOAuth(t *testing.T) {
	st, id := reliabilityStore(t)
	a, err := st.GetAccountByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOutcome(st, a, "check", "ok", "", map[string]any{"accessToken": "stale"}); err != nil {
		t.Fatal(err)
	}
	current, _ := st.GetAccountByID(id)
	if current.AccessToken != "" {
		t.Fatal("non-OAuth step overwrote credential")
	}
	st.UpdateState(id, "registered", "fixture", "")
	a, _ = st.GetAccountByID(id)
	if err := ApplyOutcome(st, a, "oauth", "ok", "", map[string]any{"accessToken": "partial"}); err == nil {
		t.Fatal("partial OAuth credentials accepted")
	}
	current, _ = st.GetAccountByID(id)
	if current.AccessToken != "" || current.State != "registered" {
		t.Fatal("partial OAuth outcome persisted")
	}
	values := map[string]any{"accessToken": "fixture-access", "refreshToken": "fixture-refresh", "expiresAt": int64(1900000000000), "scopes": []string{"user:profile"}}
	if err := ApplyOutcome(st, a, "oauth", "ok", "", values); err != nil {
		t.Fatal(err)
	}
	current, _ = st.GetAccountByID(id)
	if current.State != "subscribed" || current.ExpiresAt != 1900000000000 {
		t.Fatal("native credential expiry or lifecycle lost")
	}
}
func TestOutcomeRollbackOnEventFailure(t *testing.T) {
	st, id := reliabilityStore(t)
	a, _ := st.GetAccountByID(id)
	st.DB().Exec(`CREATE TRIGGER reject_event BEFORE INSERT ON events BEGIN SELECT RAISE(ABORT,'fixture'); END`)
	if err := ApplyOutcome(st, a, "check", "ok", "", map[string]any{"plan": "fixture"}); err == nil {
		t.Fatal("event failure ignored")
	}
	current, _ := st.GetAccountByID(id)
	if current.Plan != "" {
		t.Fatal("credential update escaped rolled-back outcome")
	}
}

func TestManualNativeJobIsRecoveredWithoutReplay(t *testing.T) {
	st, id := reliabilityStore(t)
	job, err := st.StartManualJob(id, "check,oauth")
	if err != nil {
		t.Fatal(err)
	}
	if count, err := st.RecoverInterruptedJobs(); err != nil || count != 1 {
		t.Fatal("manual interruption not recovered")
	}
	var state, reason string
	if err := st.DB().QueryRow(`SELECT state,reason FROM jobs WHERE id=?`, job).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != "blocked" || reason != "interrupted-unknown-outcome" {
		t.Fatal("manual HTTP operation may be replayed")
	}
	if reason, err := st.SchedulerPause(); err != nil || reason == "" {
		t.Fatal("manual interruption did not pause scheduling")
	}
}
func TestPrepareReloginTransitionsBeforeSuccess(t *testing.T) {
	st, id := reliabilityStore(t)
	for _, state := range []string{"pending", "session_expired", "register_failed"} {
		if err := st.UpdateState(id, state, "fixture", ""); err != nil {
			t.Fatal(err)
		}
		account, _ := st.GetAccountByID(id)
		if err := PrepareStep(st, account, "register"); err != nil {
			t.Fatal(err)
		}
		if err := ApplyOutcome(st, account, "register", "ok", "", nil); err != nil {
			t.Fatal(err)
		}
		if account.State != "registered" {
			t.Fatal("relogin state not committed")
		}
	}
}

func TestOnboardPreservesEstablishedLifecycle(t *testing.T) {
	for _, state := range []string{"registered", "aging", "warm", "synced", "subscribed"} {
		t.Run(state, func(t *testing.T) {
			st, id := reliabilityStore(t)
			if _, err := st.DB().Exec("UPDATE accounts SET state=? WHERE id=?", state, id); err != nil {
				t.Fatal(err)
			}
			a, err := st.GetAccountByID(id)
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := ApplyOutcome(st, a, "onboard", "ok", "", nil); err != nil {
					t.Fatal(err)
				}
				saved, err := st.GetAccountByID(id)
				if err != nil {
					t.Fatal(err)
				}
				if saved.State != state {
					t.Fatalf("state regressed: %s -> %s", state, saved.State)
				}
			}
		})
	}
}

func TestRegistrationBrowserRejectionLeavesRegistering(t *testing.T) {
	st, id := reliabilityStore(t)
	if _, err := st.DB().Exec("UPDATE accounts SET state='registering' WHERE id=?", id); err != nil {
		t.Fatal(err)
	}
	a, err := st.GetAccountByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyOutcome(st, a, "register", "blocked", "blocked:browser-verification-failed", nil); err != nil {
		t.Fatal(err)
	}
	if a.State != "register_failed" {
		t.Fatalf("got %s", a.State)
	}
}
