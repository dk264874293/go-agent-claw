package feishu

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/observe"
)

func TestApprovalObservationOutcomesAndPrivacy(t *testing.T) {
	for _, decision := range []string{"allowed", "rejected", "notification_failed", "cancelled", "expired"} {
		t.Run(decision, func(t *testing.T) {
			dir := t.TempDir()
			ctx, attempt := observe.Start(context.Background(), observe.Options{WorkDir: t.TempDir(), Entry: "feishu"}, observe.Config{Dir: dir})
			id := observe.Identity(ctx).AttemptID
			ctx = observe.WithAction(ctx, "action")
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			if decision == "expired" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 30*time.Millisecond)
				defer stop()
			}
			m := &approvalManager{}
			err := m.wait(ctx, approvalTarget{ChatID: "秘密群账号", ActorID: "秘密用户", ActionID: "action", Tool: "write_file"}, []string{"审批人"}, func(_ context.Context, approvalID string, _ time.Time) error {
				switch decision {
				case "allowed", "rejected":
					if !m.resolve("秘密群账号", "审批人", approvalID, decision == "allowed") {
						t.Fatal("无法确认审批")
					}
				case "notification_failed":
					return errors.New("不应进入观测的错误正文")
				case "cancelled":
					cancel()
				}
				return nil
			})
			if (err == nil) != (decision == "allowed") {
				t.Fatalf("审批结果错误: %v", err)
			}
			attempt.Returned(observe.Payload{})
			attempt.Close()
			data, err := os.ReadFile(filepath.Join(dir, id, "events.jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{"秘密群账号", "秘密用户", "审批人", "不应进入观测的错误正文"} {
				if strings.Contains(string(data), secret) {
					t.Fatal("观测泄漏审批内容")
				}
			}
			scan := bufio.NewScanner(strings.NewReader(string(data)))
			waiting, resolved := false, false
			for scan.Scan() {
				var e observe.Event
				if err := json.Unmarshal(scan.Bytes(), &e); err != nil {
					t.Fatal(err)
				}
				if e.EventType == "approval_waiting" {
					waiting = e.Payload.ExpiresAt != nil
				}
				if e.EventType == "approval_resolved" {
					resolved = e.Payload.Decision == decision && e.ActionID == "action"
				}
			}
			if !waiting || !resolved {
				t.Fatalf("审批观测不完整: %s", data)
			}
		})
	}
}
