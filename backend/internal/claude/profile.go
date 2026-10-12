package claude

import "context"

// Profile reads organization metadata without running a health check or KYC.
func (c *Client) Profile(ctx context.Context, session, id string) (map[string]any, error) {
	orgs, err := c.Organizations(ctx, session)
	if err != nil {
		return nil, err
	}
	org, err := selectOrganization(orgs, id)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"orgId": org.ID, "orgCreatedAt": org.CreatedAt}
	if org.Plan != nil {
		result["plan"] = *org.Plan
	}
	if org.Tier != nil {
		result["rate_limit_tier"] = *org.Tier
	}
	return result, nil
}
