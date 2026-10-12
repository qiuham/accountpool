package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"github.com/ham/accountpool/backend/internal/claude"
	"github.com/ham/accountpool/backend/internal/nettrace"
	"github.com/ham/accountpool/backend/internal/pool"
	"github.com/ham/accountpool/backend/internal/runner"
	"github.com/ham/accountpool/backend/internal/store"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

func configureTaskTrace(st *store.Store, sch *pool.Scheduler, r *runner.Runner, mail *claude.MailClient) {
	sch.StartTrace = func(id int64) func() {
		dir := filepath.Join(r.ProjectRoot, "artifacts", "http")
		_ = os.MkdirAll(dir, 0700)
		oldFiles, _ := filepath.Glob(filepath.Join(dir, "job-*.jsonl"))
		for _, old := range oldFiles {
			if info, e := os.Stat(old); e == nil && info.ModTime().Before(time.Now().Add(-7*24*time.Hour)) {
				_ = os.Remove(old)
			}
		}
		path := filepath.Join(dir, fmt.Sprintf("job-%d.jsonl", id))
		capture, err := nettrace.New(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "HTTP trace initialization failed")
			return func() {}
		}
		mail.Trace = capture
		r.Trace = capture
		r.TraceFile = path
		// Retention applies only to captured traces, not execution history.
		_, _ = st.DB().Exec("DELETE FROM job_http_traces WHERE captured_at<?", time.Now().Add(-7*24*time.Hour).Unix())
		return func() {
			mail.Trace = nil
			r.Trace = nil
			r.TraceFile = ""
			f, err := os.Open(path)
			if err != nil {
				return
			}
			defer f.Close()
			scan := bufio.NewScanner(io.LimitReader(f, 4<<20))
			scan.Buffer(make([]byte, 4096), 256<<10)
			records := []any{}
			for scan.Scan() {
				var v any
				if json.Unmarshal(scan.Bytes(), &v) == nil {
					records = append(records, nettrace.Clean(v))
				}
				if len(records) >= 200 {
					break
				}
			}
			sort.SliceStable(records, func(i, j int) bool {
				a, _ := records[i].(map[string]any)
				b, _ := records[j].(map[string]any)
				at, _ := a["at"].(string)
				bt, _ := b["at"].(string)
				ta, _ := time.Parse(time.RFC3339Nano, at)
				tb, _ := time.Parse(time.RFC3339Nano, bt)
				return ta.Before(tb)
			})
			raw, err := json.Marshal(records)
			if err != nil {
				return
			}
			_, err = st.DB().Exec("INSERT OR REPLACE INTO job_http_traces(job_id,captured_at,payload) VALUES(?,?,?)", id, time.Now().Unix(), string(raw))
			if err == nil {
				_ = os.Remove(path)
			} else {
				fmt.Fprintln(os.Stderr, "HTTP trace persistence failed")
			}
		}
	}
}
