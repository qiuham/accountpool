package lifecycle

import (
	"fmt"
	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/store"
)

// RestoreMailbox holds the same process lock as maintenance, so no login can
// race the repair. Registration evidence is retained; only mailbox state changes.
func RestoreMailbox(st *store.Store, mail *claude.MailClient, id int64) error {
	unlock, err := st.LockWorker()
	if err != nil {
		return fmt.Errorf("任务正在执行，请结束后重试")
	}
	defer unlock()
	a, err := st.GetAccountByID(id)
	if err != nil {
		return fmt.Errorf("账号不存在")
	}
	if a.RetiredAt != "" || a.State == "burned" || a.AccessToken != "" || a.RefreshToken != "" {
		return fmt.Errorf("账号不支持邮箱恢复")
	}
	if a.State != "mailbox_missing" {
		return fmt.Errorf("仅邮箱缺失的账号可恢复")
	}
	evidence, err := st.RegistrationEvidence(id)
	if err != nil {
		return err
	}
	if evidence != "confirmed" {
		return fmt.Errorf("请先确认原账号的注册证据")
	}
	if hit, err := st.IsTombstoned("email", a.Email); err != nil {
		return err
	} else if hit {
		return fmt.Errorf("邮箱已退役")
	}
	jwt, err := mail.RestoreAddress(a.Email)
	if err != nil {
		return err
	}
	// Commit identity-verified credentials and resumable state atomically.
	if err = st.CommitOutcome(id, a.State, "login_required", "mailbox-restored", "same address restored and mailbox identity verified", map[string]any{"mail_jwt": jwt, "last_error": ""}); err != nil {
		return err
	}
	if err = st.ClearAccountRetry(id); err != nil {
		return err
	}
	return st.SetAccountsPaused([]int64{id}, false)
}
