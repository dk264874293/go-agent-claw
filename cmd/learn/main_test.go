package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestCLIListsLessonsAndRejectsInvalidRequests(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
	}{
		{[]string{"-list"}, 0}, {[]string{"-help"}, 0},
		{[]string{"-scenario", "unknown"}, 1}, {[]string{"extra"}, 2}, {[]string{"-unknown"}, 2},
	} {
		var output bytes.Buffer
		if code := run(context.Background(), tc.args, &output); code != tc.code {
			t.Fatalf("参数%v退出%d：%s", tc.args, code, output.String())
		}
		if tc.code != 0 && strings.Contains(output.String(), "[课程通过]") {
			t.Fatal("错误参数打印成功")
		}
		if len(tc.args) == 1 && tc.args[0] == "-list" && !strings.Contains(output.String(), "recovery") {
			t.Fatal("课程列表缺少恢复课程")
		}
	}
}

func TestCLIDefaultRunsActualReadLoop(t *testing.T) {
	var output bytes.Buffer
	if code := run(context.Background(), nil, &output); code != 0 {
		t.Fatalf("默认回放退出%d：%s", code, output.String())
	}
	for _, expected := range []string{"[模型请求 1]", "[模型请求 2]", "[分发回执]", "status=SUCCEEDED effect=NONE", `"status":"ANSWERED"`} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("缺少观察%s：%s", expected, output.String())
		}
	}
}
