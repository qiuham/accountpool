package runner

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ham/accountpool/backend/internal/claude"
	_ "modernc.org/sqlite"
)

func pythonProject(t *testing.T) *Runner {
	t.Helper()
	root := t.TempDir()
	source, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	// Copy Python source so a test may replace only the browser step, while the
	// real CLI/adapter/IPC code still runs in nested subprocesses.
	err = filepath.WalkDir(filepath.Join(source, "executor"), func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".py" {
			return nil
		}
		rel, _ := filepath.Rel(source, path)
		target := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			return err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, raw, 0600)
	})
	if err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(root, "bundles/fixture/profile")
	os.MkdirAll(profile, 0700)
	db, err := sql.Open("sqlite", filepath.Join(profile, "cookies.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE moz_cookies(name TEXT,value TEXT,host TEXT); INSERT INTO moz_cookies VALUES('sessionKey','fixture-session','.claude.ai'),('lastActiveOrg','fixture-org','.claude.ai')`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}
	return &Runner{ProjectRoot: root, Python: python, Timeout: 10 * time.Second}
}
func TestNativeHTTPWithRealPythonSessionBridge(t *testing.T) {
	r := pythonProject(t)
	authorizeCalls, tokenCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/api/organizations":
			w.Write([]byte(`[{"uuid":"fixture-org","analytics_subscription_plan":"claude_pro","rate_limit_tier":"pro"}]`))
		case "/api/organizations/fixture-org/kyc_status":
			w.Write([]byte(`{"status":"not_required"}`))
		case "/v1/oauth/fixture-org/authorize":
			authorizeCalls++
			var body map[string]string
			json.NewDecoder(req.Body).Decode(&body)
			json.NewEncoder(w).Encode(map[string]string{"redirect_uri": "https://platform.claude.com/oauth/code/callback?code=fixture-code&state=" + url.QueryEscape(body["state"])})
		case "/token":
			tokenCalls++
			w.Write([]byte(`{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":3600,"scope":"user:profile"}`))
		default:
			t.Errorf("unexpected HTTP operation %s", req.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	r.Client = &claude.Client{HTTP: server.Client(), BaseURL: server.URL, TokenURL: server.URL + "/token"}
	result, path, err := r.Run(context.Background(), Spec{Proxy: fixtureSpec().Proxy, Platform: "claude", Account: "fixture", Steps: []string{"check", "oauth"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "ok" || len(result.Steps) != 2 || result.CredsOf().Extra["accessToken"] != "fixture-access" || authorizeCalls != 1 || tokenCalls != 1 {
		t.Fatalf("native steps not completed: %s", result.Reason)
	}
	if stat, err := os.Stat(path); err != nil || stat.Mode().Perm() != 0600 {
		t.Fatal("credential artifact permissions")
	}
	// HTTP-only steps must not import removed Python business modules.
	for _, step := range result.Steps {
		if step.Step != "check" && step.Step != "oauth" {
			t.Fatal("wrong outcome identity")
		}
	}
}
func TestMailBridgeThroughNestedPythonExecutor(t *testing.T) {
	r := pythonProject(t)
	fake := `import json,os,time
from pathlib import Path
from executor.runtime.browser_services import BrowserServices

def execute_registration_step(argv):
    assert '--mail-jwt' not in argv
    assert not os.environ.get('MAIL_ADMIN_AUTH')
    client=BrowserServices()
    try:
        client.call('mail.snapshot')
        proof=client.call('mail.wait', timeout=2)['proof']
        assert proof['link']=='https://claude.ai/magic-link#fresh'
    finally:
        client.close()
    out=Path('artifacts')/('register-fixture-'+str(time.time_ns()))
    out.mkdir(parents=True)
    (out/'report.json').write_text(json.dumps({'sessionKey':True}))
    return 0
`
	if err := os.WriteFile(filepath.Join(r.ProjectRoot, "executor/platforms/claude/registration.py"), []byte(fake), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.Header.Get("Authorization") != "Bearer fixture-mail-jwt" {
			t.Error("Go did not supply mailbox credential")
		}
		if calls == 1 {
			w.Write([]byte(`{"results":[{"id":10,"text":"https://claude.ai/magic-link#old"}]}`))
		} else {
			w.Write([]byte(`{"results":[{"id":11,"text":"https://claude.ai/magic-link#fresh"}]}`))
		}
	}))
	defer server.Close()
	r.Mail = &claude.MailClient{Base: server.URL, HTTP: server.Client()}
	t.Setenv("MAIL_ADMIN_AUTH", "must-not-reach-python")
	result, path, err := r.Run(context.Background(), Spec{Proxy: fixtureSpec().Proxy, Platform: "claude", Account: "fixture", Steps: []string{"register"}, MailJWT: "fixture-mail-jwt"})
	if err != nil {
		raw, _ := os.ReadFile(filepath.Join(filepath.Dir(path), "executor.log"))
		t.Fatalf("%v; %s", err, raw)
	}
	if result.Status != "ok" || calls != 2 {
		t.Fatalf("bridge did not complete: %s", result.Reason)
	}
	filepath.WalkDir(filepath.Dir(path), func(path string, entry os.DirEntry, err error) error {
		if filepath.Base(path) == "job.json" {
			t.Error("mail job left behind")
		}
		return nil
	})
}
