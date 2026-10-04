package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestAcceptanceDockerFailureVisibility(t *testing.T) {
	for _, phase := range []string{"create", "start", "inspect", "rm"} {
		t.Run(phase, func(t *testing.T) {
			d, calls := fakeDocker(t, `{"ExitCode":0,"Running":false}`, func(context.Context, *outputBuffer) error { return nil })
			command := d.command
			d.command = func(ctx context.Context, args []string, out *outputBuffer) error {
				if args[0] == phase {
					*calls = append(*calls, append([]string(nil), args...))
					return fmt.Errorf("注入%s失败", phase)
				}
				return command(ctx, args, out)
			}
			_, err := d.Execute(context.Background(), Request{WorkDir: t.TempDir(), Command: "测试"})
			if err == nil || !strings.Contains(err.Error(), "注入"+phase+"失败") {
				t.Fatalf("故障丢失: %v", err)
			}
			if (*calls)[len(*calls)-1][0] != "rm" {
				t.Fatal("没有回收")
			}
			for _, call := range *calls {
				if call[0] == "pull" {
					t.Fatal("自动拉取镜像")
				}
				if phase == "create" && call[0] == "start" {
					t.Fatal("创建失败后执行")
				}
			}
		})
	}
	t.Run("创建及清理同时失败", func(t *testing.T) {
		d, _ := fakeDocker(t, `{}`, func(context.Context, *outputBuffer) error { return nil })
		command := d.command
		d.command = func(ctx context.Context, args []string, out *outputBuffer) error {
			if args[0] == "create" || args[0] == "rm" {
				return errors.New("注入" + args[0] + "失败")
			}
			return command(ctx, args, out)
		}
		_, err := d.Execute(context.Background(), Request{WorkDir: t.TempDir(), Command: "测试"})
		if err == nil || !strings.Contains(err.Error(), "create失败") || !strings.Contains(err.Error(), "rm失败") {
			t.Fatal(err)
		}
	})
}

func TestAcceptanceImagePinnedAcrossRuns(t *testing.T) {
	d, calls := fakeDocker(t, `{"ExitCode":0,"Running":false}`, func(context.Context, *outputBuffer) error { return nil })
	for i := 0; i < 2; i++ {
		if _, err := d.Execute(context.Background(), Request{WorkDir: t.TempDir(), Command: "测试", Network: i == 1}); err != nil {
			t.Fatal(err)
		}
	}
	inspections, creates := 0, 0
	for _, call := range *calls {
		if call[0] == "image" {
			inspections++
		}
		if call[0] == "create" {
			creates++
			if !strings.Contains(strings.Join(call, " "), testImageID) {
				t.Fatal("镜像未固定")
			}
		}
	}
	if inspections != 1 || creates != 2 {
		t.Fatal(inspections, creates)
	}
}
