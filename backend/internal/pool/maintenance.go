package pool

import (
	"fmt"
	"strings"
	"time"

	"github.com/ham/accountpool/backend/internal/store"
)

// Maintenance keeps registered free accounts separate from paid/OAuth inventory.
// One decision enqueues at most one step. Pending work reserves capacity.
type Maintenance struct {
	EmailPrefix   string
	TargetFree    int
	CheckInterval time.Duration
	MaxNewPerDay  int
	ChatInterval  time.Duration
	ChatPrompts   []string
}
type Inventory struct {
	Free         int    `json:"free"`
	Reserved     int    `json:"reserved"`
	Paid         int    `json:"paid"`
	Unavailable  int    `json:"unavailable"`
	CreatedToday int    `json:"created_today"`
	Target       int    `json:"target"`
	Action       string `json:"action,omitempty"`
}

func (m Maintenance) Validate() error {
	if len(m.EmailPrefix) > 48 {
		return fmt.Errorf("MAIL_PREFIX must be at most 48 ASCII characters")
	}
	for _, c := range m.EmailPrefix {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return fmt.Errorf("MAIL_PREFIX accepts only ASCII letters, digits, hyphen and underscore")
		}
	}
	if m.TargetFree < 0 || m.CheckInterval < time.Hour || m.MaxNewPerDay < 1 {
		return fmt.Errorf("target-free must be nonnegative, check-interval >= 1h, max-new-per-day >= 1")
	}
	return nil
}
func paidAccount(a store.MaintenanceAccount) bool {
	return (a.Plan != "" && a.Plan != "claude_free") || a.AccessToken != "" || a.RefreshToken != "" || a.SubscribedAt != "" || a.State == string(Subscribed) || a.State == string(Active) || a.State == string(Subscribing)
}
func maintainable(a store.MaintenanceAccount) bool {
	if a.RetiredAt != "" {
		return false
	}
	switch State(a.State) {
	case MailboxCreateFailed, LoginRequired, Pending, Registering, Registered, Synced, Aging, Warm, Subscribed, Active, SessionExpired, Cooldown:
		return true
	}
	return false
}
func onboardingComplete(a store.MaintenanceAccount) bool {
	return a.Jobs["onboard"].State == "ok" || (a.Jobs["warmup"].State == "ok" && a.Jobs["warmup"].ID >= a.Jobs["register"].ID)
}
func nextMaintenanceStep(a store.MaintenanceAccount, now time.Time, interval time.Duration) string {
	if a.Paused || a.Busy || !maintainable(a) || a.AccessToken != "" || a.RefreshToken != "" {
		return "" // downstream owns OAuth accounts and refresh
	}
	if a.RetryStep != "" {
		if now.Unix() >= a.RetryAt {
			return a.RetryStep
		}
		return ""
	}
	if a.State == string(Cooldown) {
		return ""
	}
	if a.State == string(LoginRequired) {
		return "register"
	}
	// Failed/blocked mutation attempts are never silently replayed.
	if a.State == string(Pending) && !paidAccount(a) {
		if _, ok := a.Jobs["register"]; !ok {
			return "register"
		}
		return ""
	}
	if a.State == string(Registering) {
		return ""
	}
	check := a.Jobs["check"]
	reg := a.Jobs["register"]
	if a.State == string(SessionExpired) {
		if reg.ID == 0 || check.Reason == "blocked:session-expired" && check.ID > reg.ID && (reg.ID == 0 || a.LastHealthyCheckID >= reg.ID) {
			return "register"
		}
		return ""
	}
	if !paidAccount(a) && !onboardingComplete(a) {
		if _, ok := a.Jobs["onboard"]; !ok {
			return "onboard"
		}
		return ""
	}
	checked, err := time.ParseInLocation("2006-01-02 15:04:05", check.FinishedAt, time.Local)
	if paidAccount(a) && err == nil && now.Before(checked.Add(interval)) && check.State == "ok" && check.ID >= reg.ID && a.Plan != "" && a.Plan != "claude_free" {
		if _, attempted := a.Jobs["oauth"]; !attempted {
			return "oauth"
		}
	}
	if check.ID == 0 || reg.ID > check.ID {
		return "check"
	}
	// Read-only checks can run again at the configured interval. Unknown results
	// reserve the account instead of triggering unlimited replacement creation.
	if err == nil && !now.Before(checked.Add(interval)) {
		return "check"
	}
	return ""
}
func (m Maintenance) Decide(accounts []store.MaintenanceAccount, now time.Time) (Inventory, *store.MaintenanceAction, error) {
	result := m.Summary(accounts, now)
	if err := m.Validate(); err != nil {
		return result, nil, err
	}
	var action *store.MaintenanceAction
	for _, a := range accounts {
		paid := paidAccount(a)
		if action == nil && !a.Paused {
			step := nextMaintenanceStep(a, now, m.CheckInterval)
			if step == "warmup" && m.ChatInterval < time.Hour {
				step = "check"
			}
			if step == "" && m.ChatInterval >= time.Hour && !a.Busy && !paid && maintainable(a) && a.RetryStep == "" && a.State != string(Cooldown) && a.Jobs["check"].State == "ok" && onboardingComplete(a) && a.SessionKey != "" {
				previous, exists := a.Jobs["warmup"]
				finished, err := time.ParseInLocation("2006-01-02 15:04:05", previous.FinishedAt, time.Local)
				if !exists || ((previous.State == "ok" || previous.State == "failed" && a.Jobs["check"].ID > previous.ID) && err == nil && !now.Before(finished.Add(m.ChatInterval))) {
					step = "warmup"
				}
			}
			if step != "" {
				action = &store.MaintenanceAction{AccountID: a.ID, Kind: step}
			}
		}
	}
	if action != nil {
		result.Action = action.Kind
		return result, action, nil
	}
	if result.Free+result.Reserved < m.TargetFree && result.CreatedToday < m.MaxNewPerDay {
		// The store allocates the final sequence and account atomically.
		action = &store.MaintenanceAction{Kind: "register", NewAccount: &store.Account{Name: m.EmailPrefix}}
		result.Action = "create"
	}
	return result, action, nil
}
func (m Maintenance) Reconcile(st *store.Store) (Inventory, error) {
	var inventory Inventory
	err := st.ReconcileMaintenance(func(accounts []store.MaintenanceAccount) (*store.MaintenanceAction, error) {
		var action *store.MaintenanceAction
		var err error
		inventory, action, err = m.Decide(accounts, time.Now())
		return action, err
	})
	return inventory, err
}

// Summary shares the scheduler inventory rules without planning or writing work.
func (m Maintenance) Summary(accounts []store.MaintenanceAccount, now time.Time) Inventory {
	result := Inventory{Target: m.TargetFree}
	for _, a := range accounts {
		if a.AutomaticallyCreated && strings.HasPrefix(a.CreatedAt, now.Format("2006-01-02")) {
			result.CreatedToday++
		}
		paid := paidAccount(a)
		if paid {
			result.Paid++
		} else if a.Paused || a.State == string(MailboxMissing) || a.State == string(MailboxConfigError) {
			result.Reserved++
		} else if !maintainable(a) {
			result.Unavailable++
		} else {
			check := a.Jobs["check"]
			checked, parseErr := time.ParseInLocation("2006-01-02 15:04:05", check.FinishedAt, time.Local)
			if parseErr == nil && now.Before(checked.Add(m.CheckInterval)) && a.Plan == "claude_free" && a.SessionKey != "" && check.State == "ok" && onboardingComplete(a) && (a.State == string(Registered) || a.State == string(Warm) || a.State == string(Aging) || a.State == string(Synced)) {
				result.Free++
			} else {
				result.Reserved++
			}
		}
	}
	return result
}
