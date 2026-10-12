package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutorReceivesParentLockDescriptor(t *testing.T) {
	r := fixtureRunner(t, `{"platform":"claude","account":"fixture","status":"ok"}`)
	f, err := os.OpenFile(r.Python, os.O_APPEND|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString("test \"$ACCOUNTPOOL_LOCK_FD\" = 3 || exit 9\ntest -e /proc/$$/fd/3 || exit 10\n")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Run(context.Background(), fixtureSpec()); err != nil {
		t.Fatal(err)
	}
}

func TestPythonWorkerReusesGoLock(t *testing.T) {
	r := fixtureRunner(t, "")
	repo, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/usr/bin/env python3
import json,sys
from pathlib import Path
sys.path.insert(0, %q)
from executor.runtime.execution_guard import execution_guard
with execution_guard(Path(%q)):
    out=Path(sys.argv[sys.argv.index('--out')+1])
    out.write_text(json.dumps({'platform':'claude','account':'fixture','status':'ok'}))
`, repo, r.ProjectRoot)
	if err := os.WriteFile(r.Python, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.Run(context.Background(), fixtureSpec()); err != nil {
		t.Fatal(err)
	}
}
