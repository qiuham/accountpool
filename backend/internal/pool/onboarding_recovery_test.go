package pool

import (
	"database/sql"
	"github.com/ham/accountpool/backend/internal/store"
	"testing"
)

func TestOnboardingRequiredQueuesSetupNotAnotherMessage(t *testing.T) {
	st, id, _, _ := policyFixture(t)
	a, err := st.GetAccountByID(id)
	if err != nil {
		t.Fatal(err)
	}
	jobID, err := st.CreateJob(id, "warmup", sql.NullInt64{})
	if err != nil {
		t.Fatal(err)
	}
	s := &Scheduler{Store: st}
	if err = s.finish(&store.Job{ID: jobID, AccountID: id, Kind: "warmup"}, a, "failed", "onboarding-required", "", ""); err != nil {
		t.Fatal(err)
	}
	next, err := st.ClaimJob()
	if err != nil || next == nil || next.Kind != "onboard" {
		t.Fatalf("next=%+v err=%v", next, err)
	}
	if err = s.finish(next, a, "ok", "", "", ""); err != nil {
		t.Fatal(err)
	}
	next, err = st.ClaimJob()
	if err != nil || next == nil || next.Kind != "check" {
		t.Fatalf("next=%+v err=%v", next, err)
	}
}
