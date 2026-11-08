package context

import (
	"strings"
	"testing"
)

func TestOrdinarySummaryChecksStayInOriginalGeneration(t *testing.T) {
	prompt := NewPromptComposer(t.TempDir(), false).Build().Content
	for _, requirement := range []string{"当前生成轮", "分类逐项对照发现清单", "保留例外", "公式端点", "默认值", "两端版本", "就地给出文件与行号", "不另开模型轮"} {
		if !strings.Contains(prompt, requirement) {
			t.Fatalf("普通汇总缺少写前检查边界：%s", requirement)
		}
	}
	// 提示约束应适用于任意资料，不能携带评分器答案或课程数据。
	for _, fixtureAnswer := range []string{"84.5", "39.5", "97.5", "29.5", "第5节", "SDD强化训练", "15篇"} {
		if strings.Contains(prompt, fixtureAnswer) {
			t.Fatalf("生产提示混入夹具答案：%s", fixtureAnswer)
		}
	}
}
