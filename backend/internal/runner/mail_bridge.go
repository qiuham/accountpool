package runner

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/ham/accountpool/backend/internal/claude"
)

// A private inherited socket is scoped to one browser task. Mail credentials
// stay in Go; Python receives only the requested one-time login proof.
func mailBridge(ctx context.Context, client *claude.MailClient, jwt string) (*os.File, func(), error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	parent := os.NewFile(uintptr(fds[0]), "mail-parent")
	child := os.NewFile(uintptr(fds[1]), "mail-child")
	conn, err := net.FileConn(parent)
	parent.Close()
	if err != nil {
		child.Close()
		return nil, nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); defer conn.Close(); serveMailBridge(ctx, conn, client, jwt) }()
	stop := func() { cancel(); conn.Close(); child.Close(); <-done }
	return child, stop, nil
}
func serveMailBridge(ctx context.Context, conn net.Conn, client *claude.MailClient, jwt string) {
	decoder := json.NewDecoder(io.LimitReader(conn, 64<<10))
	encoder := json.NewEncoder(conn)
	var watermark int64
	ready := false
	consumed := false
	for {
		var req struct {
			Method  string `json:"method"`
			Timeout int    `json:"timeout"`
		}
		if err := decoder.Decode(&req); err != nil {
			return
		}
		response := map[string]any{}
		var err error
		if jwt == "" {
			err = &claude.MailboxError{Kind: "credential-missing"}
		} else {
			switch req.Method {
			case "mail.snapshot":
				if ready {
					err = errors.New("mailbox-snapshot-already-taken")
					break
				}
				watermark, err = client.LoginWatermark(ctx, jwt)
				ready = err == nil
			case "mail.wait":
				if !ready || consumed {
					err = errors.New("mailbox-invalid-sequence")
					break
				}
				consumed = true
				if req.Timeout < 1 || req.Timeout > 600 {
					err = errors.New("mailbox-invalid-timeout")
					break
				}
				var proof *claude.LoginProof
				proof, err = client.WaitForLogin(ctx, jwt, watermark, time.Duration(req.Timeout)*time.Second, 5*time.Second)
				if err == nil {
					response["proof"] = proof
				}
			default:
				err = errors.New("mailbox-unknown-operation")
			}
		}
		if err != nil {
			reason := "mailbox-read-failed"
			var me *claude.MailboxError
			if errors.As(err, &me) {
				reason = "mailbox-" + me.Kind
				if me.Kind == "verification-timeout" {
					reason = "mail-timeout"
				}
			}
			if strings.HasPrefix(err.Error(), "mailbox-") {
				reason = err.Error()
			}
			response["error"] = reason
		} else {
			response["ok"] = true
		}
		if encoder.Encode(response) != nil {
			return
		}
	}
}
