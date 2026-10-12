package pool

import (
	"encoding/json"
	"fmt"
	"github.com/ham/accountpool/backend/internal/store"
	"time"
)

// ApplyOutcome is shared by manual execution and the worker. Only observations
// returned by this invocation can update credentials or lifecycle state.
func ApplyOutcome(st *store.Store, acc *store.Account, kind, status, reason string, values map[string]any) error {
	fields := map[string]any{}
	for key, column := range map[string]string{"sessionKey": "session_key", "orgId": "org_id", "plan": "plan", "rate_limit_tier": "rate_limit_tier"} {
		if value, ok := values[key].(string); ok {
			fields[column] = value
		}
	}
	// OAuth credentials are one validated set. Other steps cannot overwrite them.
	if kind == "oauth" && status == "ok" {
		access, _ := values["accessToken"].(string)
		refresh, _ := values["refreshToken"].(string)
		var expires int64
		switch v := values["expiresAt"].(type) {
		case int64:
			expires = v
		case float64:
			expires = int64(v)
		case json.Number:
			expires, _ = v.Int64()
		}
		if access == "" || refresh == "" || expires <= 0 {
			return fmt.Errorf("incomplete OAuth outcome")
		}
		scopes, err := json.Marshal(values["scopes"])
		if err != nil || string(scopes) == "null" {
			return fmt.Errorf("missing OAuth scopes")
		}
		fields["access_token"] = access
		fields["refresh_token"] = refresh
		fields["expires_at"] = expires
		fields["scopes"] = string(scopes)
		fields["subscribed_at"] = time.Now().Format("2006-01-02 15:04:05")
	}
	if reason == "blocked:session-expired" {
		fields["session_key"] = ""
	}
	if values["health"] == "banned" {
		fields["banned_at"] = time.Now().Format("2006-01-02 15:04:05")
	}
	if kind == "check" {
		fields["last_check_at"] = time.Now().Format("2006-01-02 15:04:05")
	}
	fields["last_error"] = reason
	toState := acc.State
	if kind == "check" && status == "ok" && acc.State == string(Cooldown) {
		toState = string(Registered)
	}
	if kind == "register" && status == "ok" && acc.RegisteredAt == "" {
		fields["registered_at"] = time.Now().Format("2006-01-02 15:04:05")
	}
	if kind == "check" && status == "ok" && values["subscribed"] == true && CanTransition(State(acc.State), Subscribed) {
		toState = string(Subscribed)
		fields["subscribed_at"] = time.Now().Format("2006-01-02 15:04:05")
	}
	if to, ok := MappingFromRunner(kind, status, reason); ok {
		// Repeating setup confirms a prerequisite; it must not regress an
		// established account's lifecycle or subscription.
		if kind == "onboard" && status == "ok" {
			switch State(acc.State) {
			case Registered, Synced, Aging, Warm, Subscribed, Active:
				to = State(acc.State)
			}
		}
		if err := MustTransition(State(acc.State), to); err != nil {
			return err
		}
		toState = string(to)
	}
	evidence := map[string]string{"status": status, "reason": reason}
	if kind == "check" || kind == "register" {
		if id, ok := values["orgId"].(string); ok {
			evidence["orgId"] = id
		}
		if date, ok := values["orgCreatedAt"].(string); ok {
			evidence["orgCreatedAt"] = date
		}
	}
	for _, key := range []string{"metadataStatus", "metadataReason"} {
		if value, ok := values[key].(string); ok {
			evidence[key] = value
		}
	}
	detail, _ := json.Marshal(evidence)
	if err := st.CommitOutcome(acc.ID, acc.State, toState, kind, string(detail), fields); err != nil {
		return err
	}
	acc.State = toState
	return nil
}

// PrepareStep records a recoverable login attempt before browser submission.
func PrepareStep(st *store.Store, acc *store.Account, kind string) error {
	if kind != "register" && kind != "onboard" {
		return nil
	}
	if kind == "onboard" && acc.State != "pending" {
		return nil
	}
	if acc.State == "register_failed" {
		if err := st.UpdateState(acc.ID, "pending", kind, "explicit retry"); err != nil {
			return err
		}
		acc.State = "pending"
	}
	if CanTransition(State(acc.State), Registering) && acc.State != "registering" {
		if err := st.UpdateState(acc.ID, "registering", kind, "starting account setup"); err != nil {
			return err
		}
		acc.State = "registering"
	}
	return nil
}
