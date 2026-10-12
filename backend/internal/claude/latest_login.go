package claude

import (
	"context"
	"time"
)

// LatestLogin is a read-only, recent inbox snapshot; validity is decided by Claude.
type LatestLogin struct {
	Code       string `json:"code,omitempty"`
	Kind       string `json:"kind"`
	ReceivedAt string `json:"receivedAt,omitempty"`
}

func (c *MailClient) LatestLogin(ctx context.Context, jwt string, now time.Time) (*LatestLogin, error) {
	mails, err := c.ListMails(ctx, jwt, 100, 0)
	if err != nil {
		return nil, err
	}
	var newest int64 = -1
	var selected Mail
	for _, m := range mails {
		proof, e := selectLoginProof([]Mail{m}, 0)
		if e != nil {
			return nil, e
		}
		if proof == nil {
			continue
		}
		id, e := mailID(m)
		if e != nil {
			return nil, e
		}
		if id > newest {
			newest = id
			selected = m
		}
	}
	if newest < 0 {
		return &LatestLogin{Kind: "empty"}, nil
	}
	received, err := time.Parse(time.RFC3339, selected.CreatedAt)
	if err != nil {
		received, err = time.ParseInLocation("2006-01-02 15:04:05", selected.CreatedAt, time.UTC)
	}
	if err != nil {
		return &LatestLogin{Kind: "unknown_time"}, nil
	}
	result := &LatestLogin{ReceivedAt: received.Format(time.RFC3339)}
	if received.After(now) || now.Sub(received) > 10*time.Minute {
		result.Kind = "stale"
		return result, nil
	}
	proof, err := selectLoginProof([]Mail{selected}, 0)
	if err != nil {
		return nil, err
	}
	if proof.Link != "" {
		result.Kind = "link"
	} else {
		result.Kind = "code"
		result.Code = proof.Code
	}
	return result, nil
}
