package pool

import "testing"

func TestLoginStoresOrganizationDateBeforeHealthCheck(t *testing.T) {
	st, id := reliabilityStore(t)
	if err := st.UpdateState(id, "registering", "fixture", ""); err != nil {
		t.Fatal(err)
	}
	a, _ := st.GetAccountByID(id)
	if err := ApplyOutcome(st, a, "register", "ok", "", map[string]any{"sessionKey": "fixture", "orgId": "original-org", "orgCreatedAt": "2026-09-30T00:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	var date string
	err := st.DB().QueryRow("SELECT COALESCE(json_extract(detail,'$.orgCreatedAt'),'') FROM events WHERE account_id=? AND kind='register' ORDER BY id DESC LIMIT 1", id).Scan(&date)
	if err != nil || date != "2026-09-30T00:00:00Z" {
		t.Fatalf("login date lost: %q %v", date, err)
	}
	current, _ := st.GetAccountByID(id)
	if current.LastCheckAt != "" {
		t.Fatal("profile collection faked a health check")
	}
}
