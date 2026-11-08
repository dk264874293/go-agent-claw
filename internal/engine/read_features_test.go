package engine

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func TestReadFeaturesAllCombinationsIndependentAndDefaultBaseline(t *testing.T) {
	for mask := 0; mask < 8; mask++ {
		input := &ReadFeatureSpec{Version: 1, SourceViews: mask&1 != 0, LocalChecks: mask&2 != 0, ClaimFocus: mask&4 != 0}
		spec := TaskSpec{ReadFeatures: input}
		if err := NormalizeReadFeatures(&spec); err != nil {
			t.Fatal(err)
		}
		if input.Version != 1 {
			t.Fatal("规范化修改调用方配置")
		}
		want := 4
		if mask == 0 {
			want = 2
		}
		if spec.ReadContextVersion != want || (spec.ReadFeatures == nil) != (mask == 0) {
			t.Fatalf("组合%d规范化错误: %+v", mask, spec)
		}
		r := &runState{state: taskState{Spec: spec}}
		for name, enabled := range map[string]bool{"source_views": mask&1 != 0, "local_checks": mask&2 != 0, "claim_focus": mask&4 != 0} {
			if r.readFeatureEnabled(name) != enabled {
				t.Fatalf("组合%d的%s被其他功能联动", mask, name)
			}
		}
		if r.readFeaturesV2() != (mask != 0) {
			t.Fatalf("组合%d协议开关错误", mask)
		}
	}
	spec := TaskSpec{}
	if NormalizeReadFeatures(&spec) != nil || spec.ReadContextVersion != 2 || spec.ReadFeatures != nil {
		t.Fatalf("新普通任务未回baseline默认: %+v", spec)
	}
	legacy := &runState{state: taskState{Spec: TaskSpec{ReadContextVersion: 3}}}
	for _, name := range []string{"source_views", "local_checks", "claim_focus"} {
		if !legacy.readFeatureEnabled(name) {
			t.Fatal("历史3组合改变")
		}
	}
	if legacy.readFeaturesV2() {
		t.Fatal("历史3被升级为新契约")
	}
}

func TestReadFeaturesValidationBeforePersistenceAndNoImplicitUpgrade(t *testing.T) {
	for _, bad := range []TaskSpec{
		{ReadFeatures: &ReadFeatureSpec{LocalChecks: true}},
		{ReadContextVersion: 5}, {ReadContextVersion: 4},
		{ReadContextVersion: 2, ReadFeatures: &ReadFeatureSpec{Version: 1, LocalChecks: true}},
		{ReadContextVersion: 3, ReadFeatures: &ReadFeatureSpec{Version: 1, LocalChecks: true}},
		{ReadFeatures: &ReadFeatureSpec{Version: 2}},
		{Analysis: true, ReadFeatures: &ReadFeatureSpec{SourceViews: true}},
		{Report: &ReportSpec{}, ReadFeatures: &ReadFeatureSpec{ClaimFocus: true}},
		{Quality: &QualitySpec{}, ReadFeatures: &ReadFeatureSpec{LocalChecks: true}},
	} {
		if NormalizeReadFeatures(&bad) == nil {
			t.Fatalf("接受了非法新配置: %+v", bad)
		}
	}
	for _, bad := range []TaskSpec{
		{ReadContextVersion: 4},
		{ReadContextVersion: 4, ReadFeatures: &ReadFeatureSpec{Version: 0, LocalChecks: true}},
		{ReadContextVersion: 4, ReadFeatures: &ReadFeatureSpec{Version: 1}},
		{ReadContextVersion: 3, ReadFeatures: &ReadFeatureSpec{Version: 1, SourceViews: true}},
	} {
		if ValidateReadFeatures(bad) == nil {
			t.Fatalf("持久状态被补默认值: %+v", bad)
		}
	}
	for _, s := range []TaskSpec{{Analysis: true}, {Report: &ReportSpec{}}, {Quality: &QualitySpec{Version: 1}}} {
		if NormalizeReadFeatures(&s) != nil || s.ReadContextVersion != 2 {
			t.Fatal("严格模式默认改变")
		}
	}
}

func TestReadFeatureSwitchParsingPreservesExplicitOffAndRejectsUnknown(t *testing.T) {
	if v, err := ParseReadFeatureSwitches(nil); err != nil || v != nil {
		t.Fatal("未设置的参数产生显式配置")
	}
	if v, err := ParseReadFeatureSwitches(map[string]string{"local_checks": "off"}); err != nil || v == nil || v.LocalChecks {
		t.Fatal("显式off丢失")
	}
	for _, input := range []map[string]string{{"local_checks": ""}, {"local_checks": "auto"}, {"other": "on"}, {"claim_focus": "ON"}} {
		if _, err := ParseReadFeatureSwitches(input); err == nil {
			t.Fatalf("错误开关被接受: %+v", input)
		}
	}
}

func TestReadFeaturesJSONRequiresCompleteCanonicalObject(t *testing.T) {
	for _, raw := range []string{
		`{"version":1,"local_checks":true}`,
		`{"version":1,"source_views":false,"local_checks":true,"claim_focus":null}`,
		`{"version":1,"source_views":false,"local_checks":true,"claim_focus":false,"unknown":false}`,
		`{"source_views":false,"local_checks":true,"claim_focus":false}`,
		`{"version":1,"source_views":"off","local_checks":true,"claim_focus":false}`,
	} {
		var f ReadFeatureSpec
		if json.Unmarshal([]byte(raw), &f) == nil {
			t.Fatalf("接受了非完整或有歧义的JSON: %s", raw)
		}
	}
	var f ReadFeatureSpec
	if err := json.Unmarshal([]byte(`{"version":1,"source_views":false,"local_checks":true,"claim_focus":false}`), &f); err != nil || f.Version != 1 || !f.LocalChecks || f.SourceViews || f.ClaimFocus {
		t.Fatalf("完整对象读取错误: %+v %v", f, err)
	}
}

func TestReadFeatureResumeKeepsLegacyAndRejectsExplicitChanges(t *testing.T) {
	for _, version := range []int{0, 1, 2, 3, 4} {
		dir := t.TempDir()
		store, err := taskstore.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		eng := NewAgentEngine(nil, nil, EngineOptions{Mode: tools.ModeWorkspace})
		cfg := TaskSpec{ID: taskstore.NewID(), Goal: "读取资料", ReadContextVersion: version}
		if version == 4 {
			cfg.ReadFeatures = &ReadFeatureSpec{Version: 1, SourceViews: true}
		}
		task, state, err := eng.openTask(store, dir, "会话", cfg)
		if err != nil {
			t.Fatal(err)
		}
		if version == 0 {
			state.Spec.ReadContextVersion = 0
		}
		state.Usage.ModelCalls = 3
		if _, err := task.Append("created", state); err != nil {
			t.Fatal(err)
		}
		if err := task.SaveSnapshot(state); err != nil {
			t.Fatal(err)
		}
		if err := task.Close(); err != nil {
			t.Fatal(err)
		}
		opened, resumed, err := eng.openTask(store, dir, "会话", TaskSpec{ID: cfg.ID, Resume: true})
		if err != nil {
			t.Fatal(err)
		}
		if resumed.Spec.ReadContextVersion != version || resumed.Usage.ModelCalls != 3 {
			t.Fatal("恢复升级策略或重置预算")
		}
		opened.Close()
		if _, _, err := eng.openTask(store, dir, "会话", TaskSpec{ID: cfg.ID, Resume: true, ReadContextVersion: 5}); err == nil {
			t.Fatal("恢复忽略显式协议冲突")
		}
		if version != 4 {
			if _, _, err := eng.openTask(store, dir, "会话", TaskSpec{ID: cfg.ID, Resume: true, ReadFeatures: &ReadFeatureSpec{Version: 1, SourceViews: true, LocalChecks: true, ClaimFocus: true}}); err == nil {
				t.Fatal("旧任务可启用新配置")
			}
		} else {
			if _, _, err := eng.openTask(store, dir, "会话", TaskSpec{ID: cfg.ID, Resume: true, ReadFeatures: &ReadFeatureSpec{Version: 1, LocalChecks: true}}); err == nil {
				t.Fatal("新任务恢复可改配置")
			}
			opened, _, err := eng.openTask(store, dir, "会话", TaskSpec{ID: cfg.ID, Resume: true, ReadFeatures: &ReadFeatureSpec{Version: 1, SourceViews: true}})
			if err != nil {
				t.Fatal(err)
			}
			opened.Close()
		}
		store.Close()
	}
}

func TestReadFeatureTransitionUsesSameGuardForEventsAndInspection(t *testing.T) {
	spec := TaskSpec{ID: taskstore.NewID(), Goal: "资料", Mode: tools.ModeWorkspace, Kind: TaskChange, ReadContextVersion: 4, ReadFeatures: &ReadFeatureSpec{Version: 1, SourceViews: true}}
	before := taskState{Version: 1, Spec: spec}
	makeEvent := func(seq uint64, state taskState) taskstore.Event {
		raw, _ := json.Marshal(state)
		return taskstore.Event{Version: 1, Sequence: seq, Kind: "created", Payload: raw}
	}
	var view Inspection
	if err := view.Apply(makeEvent(1, before)); err != nil {
		t.Fatal(err)
	}
	after := before
	f := *spec.ReadFeatures
	f.LocalChecks = true
	after.Spec.ReadFeatures = &f
	if err := validateReadFeatureTransition(before, after); err == nil {
		t.Fatal("转换验证放行开关变更")
	}
	if err := view.Apply(makeEvent(2, after)); err == nil || !strings.Contains(err.Error(), "读取实验") {
		t.Fatalf("Inspection未共用转换规则: %v", err)
	}
	store, err := taskstore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task, err := store.CreateTask(spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer task.Close()
	if _, err := task.Append("created", before); err != nil {
		t.Fatal(err)
	}
	if _, err := task.Append("model", after); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTaskState(task); err == nil || !strings.Contains(err.Error(), "读取实验") {
		t.Fatalf("日志恢复未共用转换规则: %v", err)
	}
}
