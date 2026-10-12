package lifecycle

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/runner"
	"github.com/ham/accountpool/backend/internal/store"
)

// Retire first publishes a durable inactive tombstone. Purge is a separate,
// explicit action after downstream deactivation; OAuth tombstones are retained.
func Retire(ctx context.Context, st *store.Store, r *runner.Runner, mail *claude.MailClient, name string, purge, downstreamDisabled bool) error {
	unlock, err := st.LockWorker()
	if err != nil {
		return err
	}
	defer unlock()
	return r.WithLock(ctx, func(_ *os.File) error {
		account, err := st.GetAccount(name)
		if err != nil {
			return err
		}
		if purge && account.State != "burned" {
			return fmt.Errorf("retire account before purging")
		}
		if purge && account.AccessToken != "" && !downstreamDisabled {
			return fmt.Errorf("confirm downstream deactivation with --downstream-disabled before purge")
		}
		if !purge {
			return st.CommitOutcome(account.ID, account.State, "burned", "retire", "inactive tombstone; resources retained", map[string]any{"retired_at": time.Now().Format("2006-01-02 15:04:05")})
		}
		root, err := filepath.EvalSymlinks(r.ProjectRoot)
		if err != nil {
			return err
		}
		bundles := filepath.Join(root, "bundles")
		bundle := account.BundlePath
		if bundle == "" {
			bundle = filepath.Join(bundles, name)
		} else if !filepath.IsAbs(bundle) {
			bundle = filepath.Join(root, bundle)
		}
		bundle = filepath.Clean(bundle)
		if filepath.Dir(bundle) != bundles || filepath.Base(bundle) != name || strings.ContainsAny(name, "/\\") {
			return fmt.Errorf("invalid account bundle path")
		}
		if info, err := os.Lstat(bundles); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("bundle root is a symlink")
		}
		if info, err := os.Lstat(bundle); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("account bundle is a symlink")
		}
		if err := st.AddTombstone("email", account.Email, "retired"); err != nil {
			return err
		}
		if err := st.AddTombstone("account", name, "retired"); err != nil {
			return err
		}
		if account.Email != "" {
			if mail == nil {
				return fmt.Errorf("mailbox client required for purge")
			}
			id, err := mail.FindAddressID(account.Email)
			if err != nil {
				return err
			}
			if id != 0 {
				if err := mail.DeleteAddress(id); err != nil {
					return err
				}
			}
		}
		if err := os.RemoveAll(bundle); err != nil {
			return err
		}
		return st.CommitOutcome(account.ID, "burned", "burned", "purge", "mailbox and browser profile removed; OAuth tombstone retained", map[string]any{"mail_jwt": "", "session_key": ""})
	})
}
