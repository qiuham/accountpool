package claude

import "testing"

func TestOrganizationCreationDateUsesSelectedOrganization(t *testing.T) {
	orgs := []Organization{{ID: "a", CreatedAt: "2020-01-01T00:00:00Z"}, {ID: "b", CreatedAt: "2024-06-01T00:00:00Z"}}
	got, err := ClassifyHealth(orgs, "b")
	if err != nil || got["orgCreatedAt"] != orgs[1].CreatedAt {
		t.Fatal(got, err)
	}
	got, err = ClassifyHealth([]Organization{{ID: "empty"}}, "")
	if err != nil || got["orgCreatedAt"] != "" {
		t.Fatal(got, err)
	}
}
