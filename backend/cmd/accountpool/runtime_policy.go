package main

import (
	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/pool"
	"github.com/ham/accountpool/backend/internal/runner"
	"github.com/ham/accountpool/backend/internal/store"
	"time"
)

func runtimePolicy(st *store.Store, sch *pool.Scheduler, r *runner.Runner) func() error {
	return func() error {
		v, err := st.RuntimeSettings()
		if err != nil {
			return err
		}
		sch.MinGap = time.Duration(v.GapSeconds) * time.Second
		sch.BaseCooldown = time.Duration(v.CooldownSeconds) * time.Second
		sch.ConsecutiveFailMax = v.FailureLimit
		r.Timeout = time.Duration(v.TaskTimeoutSeconds) * time.Second
		r.Cooldown = sch.MinGap
		supply, err := st.SupplySettings()
		if err != nil {
			return err
		}
		if sch.Maintenance != nil {
			sch.Maintenance.TargetFree = supply.Target
			sch.Maintenance.MaxNewPerDay = supply.Daily
			sch.Maintenance.CheckInterval = time.Duration(supply.CheckHours) * time.Hour
			sch.Maintenance.EmailPrefix = supply.Prefix
			activity, err := st.ActivitySettings()
			if err != nil {
				return err
			}
			sch.Maintenance.ChatInterval = 0
			if activity.Enabled {
				sch.Maintenance.ChatInterval = time.Duration(activity.IntervalHours) * time.Hour
			}
			sch.Maintenance.ChatPrompts = activity.Prompts
		}
		mail := claude.NewMailClient()
		mail.Base = supply.MailBase
		mail.AdminAuth = supply.MailPassword
		mail.Domain = supply.RegistrationDomain
		r.Mail = mail
		configureTaskTrace(st, sch, r, mail)
		sch.Mail = mailAdapter{mc: mail}
		return nil
	}
}
