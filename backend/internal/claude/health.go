package claude

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
)

type Organization struct {
	CreatedAt      string  `json:"created_at"`
	ID             string  `json:"uuid"`
	RavenType      string  `json:"raven_type"`
	Plan           *string `json:"analytics_subscription_plan"`
	Tier           *string `json:"rate_limit_tier"`
	DisabledReason string  `json:"api_disabled_reason"`
	DisabledUntil  *string `json:"api_disabled_until"`
	AccessBlock    *struct {
		Reason string `json:"reason"`
	} `json:"access_block"`
}

func (c *Client) Organizations(ctx context.Context, session string) ([]Organization, error) {
	var orgs []Organization
	if session == "" {
		return nil, &APIError{"blocked:session-expired", 0}
	}
	if err := c.request(ctx, http.MethodGet, c.BaseURL+"/api/organizations", session, nil, &orgs); err != nil {
		return nil, err
	}
	if len(orgs) == 0 {
		return nil, &APIError{"empty-organizations", 200}
	}
	for _, o := range orgs {
		if o.ID == "" {
			return nil, &APIError{"invalid-organizations", 200}
		}
	}
	return orgs, nil
}
func selectOrganization(orgs []Organization, id string) (Organization, error) {
	if id != "" {
		for _, org := range orgs {
			if org.ID == id {
				return org, nil
			}
		}
		return Organization{}, fmt.Errorf("selected organization not found")
	}
	for _, org := range orgs {
		if org.RavenType == "team" {
			return org, nil
		}
	}
	if len(orgs) == 0 {
		return Organization{}, fmt.Errorf("empty organizations")
	}
	return orgs[0], nil
}

// Health is domain evidence, not a browser action. Missing signals stay unknown.
func ClassifyHealth(orgs []Organization, id string) (map[string]any, error) {
	primary, err := selectOrganization(orgs, id)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"health": "unknown", "orgId": primary.ID, "orgCreatedAt": primary.CreatedAt}
	if primary.Plan != nil && *primary.Plan != "" {
		result["plan"] = *primary.Plan
		result["subscribed"] = *primary.Plan != "claude_free"
		if *primary.Plan == "claude_free" {
			result["health"] = "not_subscribed"
		} else {
			result["health"] = "ok"
		}
	}
	if primary.Tier != nil {
		result["rate_limit_tier"] = *primary.Tier
	}
	for _, org := range orgs {
		if org.DisabledReason != "" {
			if org.DisabledUntil == nil || *org.DisabledUntil == "" {
				result["health"] = "banned"
			} else if result["health"] != "banned" {
				result["health"] = "restricted"
			}
		}
		if org.AccessBlock != nil && org.AccessBlock.Reason != "" {
			result["health"] = "banned"
		}
	}
	return result, nil
}
func (c *Client) Check(ctx context.Context, session, orgID string) (map[string]any, error) {
	orgs, err := c.Organizations(ctx, session)
	if err != nil {
		return nil, err
	}
	result, err := ClassifyHealth(orgs, orgID)
	if err != nil {
		return nil, err
	}
	if result["health"] == "banned" || result["health"] == "restricted" {
		return result, nil
	}
	var kyc struct {
		Status string `json:"status"`
	}
	if err := c.request(ctx, http.MethodGet, c.BaseURL+"/api/organizations/"+url.PathEscape(result["orgId"].(string))+"/kyc_status", session, nil, &kyc); err != nil {
		return nil, err
	}
	if kyc.Status == "" {
		return nil, &APIError{"invalid-kyc-response", 200}
	}
	result["kyc"] = kyc.Status
	if kyc.Status != "not_required" && kyc.Status != "approved" {
		result["health"] = "needs_verification"
	}
	return result, nil
}
