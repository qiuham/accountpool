package main

import (
	"errors"
	"fmt"

	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/store"
)

// ensureMailbox validates saved credentials or recovers them through the mailbox
// administrator. Existing addresses are never recreated to replace a lost JWT.
func ensureMailbox(st *store.Store, mc *claude.MailClient, a *store.Account) (string, error) {
	cfg, err := st.SupplySettings()
	if err != nil {
		return "", err
	}
	if a.Email != "" && !cfg.AcceptsMailbox(a.Email) {
		return "", &claude.MailboxError{Kind: "unsupported-domain"}
	}
	if a.MailJWT != "" {
		err := mc.ValidateMailboxJWT(a.MailJWT, a.Email)
		if err == nil {
			return a.MailJWT, nil
		}
		var mailboxErr *claude.MailboxError
		if !errors.As(err, &mailboxErr) || mailboxErr.Kind != "credential-invalid" {
			return "", err
		}
	}
	if a.Email != "" {
		jwt, err := mc.RecoverMailboxJWT(a.Email)
		if err != nil {
			return "", err
		}
		if err := st.UpdateFields(a.ID, map[string]any{"mail_jwt": jwt}); err != nil {
			return "", err
		}
		a.MailJWT = jwt
		st.AddEvent(a.ID, "mailbox-credentials-recovered", a.State, a.State, "admin recovery verified")
		return jwt, nil
	}

	// 建之前先查墓碑：这个别名是不是被封号用过
	name := a.Name
	if hit, _ := st.IsTombstoned("email", name+"@"+mc.Domain); hit {
		return "", fmt.Errorf("%s@%s 在墓碑里（被封号用过），不能重用", name, mc.Domain)
	}

	created, err := mc.NewAddress(name)
	if err != nil {
		return "", fmt.Errorf("建地址: %w", err)
	}
	if created.JWT == "" {
		return "", fmt.Errorf("mailbox:missing-created-jwt")
	}

	upd := map[string]any{
		"email":    created.Address,
		"mail_jwt": created.JWT,
	}
	if err := st.UpdateFields(a.ID, upd); err != nil {
		return "", fmt.Errorf("邮箱建好了但入库失败（jwt 会丢，要手工补）: %w", err)
	}
	// 同步到内存里的 account，调用方后续还能用
	a.Email, a.MailJWT = created.Address, created.JWT

	st.AddEvent(a.ID, "mailbox", a.State, a.State,
		fmt.Sprintf(`{"address":"%s","id":%d}`, created.Address, created.AddressID))
	return created.JWT, nil
}
