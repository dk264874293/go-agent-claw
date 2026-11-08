package engine

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ctxpkg "github.com/dk264874293/go-agent-claw/internal/context"
	"github.com/dk264874293/go-agent-claw/internal/quality"
	"github.com/dk264874293/go-agent-claw/internal/schema"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"github.com/dk264874293/go-agent-claw/internal/tools"
)

func qualityUnknownTask(t *testing.T, dir, stage string) string {
	t.Helper()
	id := newID()
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	spec := qualitySpec(false)
	spec.ID = id
	spec.Delegation = "off"
	spec.ReadContextVersion = 2
	spec.ReadPageBytes = 8000
	spec.OutputStyle = "concise"
	q := &QualityState{AgentID: newID(), CheckedArtifactID: newID(), Version: 1, Status: "checked", DraftID: newID(), DraftArtifactID: newID(), DraftSHA256: fmt.Sprintf("%x", sha256.Sum256([]byte("{}"))), OriginRunID: newID(), CheckResult: "pass", ReviewStatus: "not_run", RevisionStatus: "not_run"}
	if stage == "checking" {
		q.Status = "checking"
		q.CheckedArtifactID = ""
	} else if stage == "quality_review" {
		q.ReviewStatus = "admitted"
		q.ReviewRunID = q.OriginRunID
	} else {
		q.ReviewStatus = "skipped"
		q.RevisionStatus = "admitted"
		q.RevisionRunID = q.OriginRunID
	}
	q.MaterialHashes = map[string]string{q.DraftArtifactID: q.DraftSHA256}
	if q.CheckedArtifactID != "" {
		q.MaterialHashes[q.CheckedArtifactID] = q.DraftSHA256
	}
	s := taskState{Version: 2, Spec: spec, WorkDir: canonicalDir, SessionID: "quality-failure", Limits: Budget{MaxModelCalls: 6, MaxToolCalls: 6, MaxTurns: 6, Timeout: time.Minute}, Usage: Usage{ModelCalls: 2, ToolCalls: 1, Turns: 2}, Quality: q, EffectLedger: &Delivery{HistoryComplete: true}}
	store, err := taskstore.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	task, err := store.CreateTask(id)
	if err != nil {
		t.Fatal(err)
	}
	if err = task.SaveArtifact(q.DraftArtifactID, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if _, err = task.Append("started", s); err != nil {
		t.Fatal(err)
	}
	if err = task.SaveSnapshot(s); err != nil {
		t.Fatal(err)
	}
	if err = task.Close(); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestQualityUnknownAdmissionResumeNeverReissues(t *testing.T) {
	for _, stage := range []string{"checking", "quality_review", "quality_revision"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			id := qualityUnknownTask(t, dir, stage)
			calls := 0
			model := reportModel(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				return nil, errors.New("不应重新请求")
			})
			eng := NewAgentEngine(model, NewExplorerRegistry(dir), EngineOptions{})
			for attempt := 0; attempt < 2; attempt++ {
				res, err := eng.Run(context.Background(), ctxpkg.NewSession("quality-failure", dir), TaskSpec{ID: id, Resume: true}, nil)
				if err != nil || res.Status != Blocked || res.StopCode != "quality_outcome_unknown" || calls != 0 || res.Usage.ModelCalls != 2 || res.Usage.ToolCalls != 1 {
					t.Fatalf("恢复%d误重发/错误终态：%+v calls=%d err=%v", attempt, res, calls, err)
				}
			}
		})
	}
}

func TestQualityAdmissionStorageFailureNeverInvokesProvider(t *testing.T) {
	for _, stage := range []string{"quality_review", "quality_revision"} {
		t.Run(stage, func(t *testing.T) {
			q := &QualityState{Version: 1, Status: "checked", DraftID: newID(), ReviewStatus: "not_run", RevisionStatus: "not_run"}
			r := &runState{started: time.Now(), runID: newID(), state: taskState{Quality: q, Limits: Budget{MaxTurns: 2, MaxModelCalls: 2, MaxToolCalls: 2, Timeout: time.Minute}}}
			admissionWrites := 0
			r.persist = func(kind string, s taskState) error {
				if kind == stage+"_admitted" {
					admissionWrites++
					if s.Usage.ModelCalls != 1 || s.Usage.Turns != 1 {
						t.Fatalf("阶段准入与计数不在同一事件：%+v", s.Usage)
					}
					return errors.New("故障注入：阶段事件写入失败")
				}
				return nil
			}
			calls := 0
			eng := NewAgentEngine(reportModel(func(context.Context, []schema.Message, []schema.ToolDefinition) (*schema.Message, error) {
				calls++
				return reply("{}")
			}), nil, EngineOptions{})
			for attempt := 0; attempt < 2; attempt++ {
				_, err := eng.qualityModel(context.Background(), r, stage, "短请求", []byte("{}"), nil)
				if !errors.Is(err, errStorage) || calls != 0 || admissionWrites != 1 || r.state.Usage.ModelCalls != 1 || r.state.Usage.Turns != 1 {
					t.Fatalf("存储失败后仍调用或重扣：calls=%d writes=%d state=%+v err=%v", calls, admissionWrites, r.state.Usage, err)
				}
			}
		})
	}
}

func TestQualityDuplicateDraftDoesNotReplaceTicket(t *testing.T) {
	id := newID()
	r := &runState{started: time.Now(), state: taskState{Quality: &QualityState{Version: 1, Status: "checked", DraftID: id}}}
	writes := 0
	r.artifact = func(string, []byte) error { writes++; return nil }
	_, err := r.checkQualityDraft(context.Background(), json.RawMessage(`{"schema_version":1,"findings":[]}`))
	var detail reportError
	if !errors.As(err, &detail) || detail.code != "quality_contract_invalid" || r.state.Quality.DraftID != id || writes != 0 {
		t.Fatalf("重复核验覆盖票据或写入新材料：id=%s writes=%d err=%v", r.state.Quality.DraftID, writes, err)
	}
}

func TestQualitySnapshotUpdateFailureRecoversCommittedEvents(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "原文事实\n")
	calls := 0
	snapshotPath := ""
	model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("r", "source.md"))
		}
		if modelStage(ctx) == "quality_revision" {
			raw, _ := json.Marshal(QualitySubmission{DraftID: currentRun(ctx).state.Quality.DraftID, Corrections: []QualityCorrection{}})
			return reply(string(raw))
		}
		r := currentRun(ctx)
		r.mu.Lock()
		persist := r.persist
		r.persist = func(kind string, s taskState) error {
			if err := persist(kind, s); err != nil {
				return err
			}
			if kind == "finished" {
				snapshotPath = filepath.Join(dir, ".claw", "tasks", s.Spec.ID, "snapshot.json")
				return os.Mkdir(snapshotPath, 0700)
			}
			return nil
		}
		r.mu.Unlock()
		return qualityCall("verify_findings", "v", qualityTestDraft(analysisIntegrationEvidence(ctx)[0]))
	})
	eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{})
	res, err := eng.Run(context.Background(), ctxpkg.NewSession("q-snapshot", dir), qualitySpec(false), nil)
	if err != nil || res.Status != Answered || calls != 3 || snapshotPath == "" {
		t.Fatalf("快照故障改写执行结果：%+v %v calls=%d", res, err, calls)
	}
	if info, err := os.Stat(snapshotPath); err != nil || !info.IsDir() {
		t.Fatalf("没有触发快照提交失败：%v", err)
	}
	// 清除临时文件系统故障；任务日志与证据保持不变，不补造快照。
	if err = os.Remove(snapshotPath); err != nil {
		t.Fatal(err)
	}
	resumed, err := eng.Run(context.Background(), ctxpkg.NewSession("q-snapshot", dir), TaskSpec{ID: res.TaskID, Resume: true}, nil)
	if err != nil || resumed.Status != Answered || calls != 3 || resumed.Usage.ModelCalls != 3 {
		t.Fatalf("未从完整事件恢复或重复调用：%+v %v calls=%d", resumed, err, calls)
	}
}

func TestQualityLastToolQuotaCannotPublishOrAddClosingCalls(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "材料事实\n")
	calls := 0
	model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("r", "source.md"))
		}
		if modelStage(ctx) == "quality_revision" {
			return qualityCall("write_report", "p", QualitySubmission{DraftID: currentRun(ctx).state.Quality.DraftID, Corrections: []QualityCorrection{}})
		}
		if calls == 2 {
			return qualityCall("verify_findings", "v", qualityTestDraft(analysisIntegrationEvidence(ctx)[0]))
		}
		t.Fatal("质量预算停止后新增了收尾请求")
		return nil, nil
	})
	eng := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace, Budget: Budget{MaxModelCalls: 3, MaxToolCalls: 2, MaxTurns: 3, Timeout: time.Minute}})
	res, err := eng.Run(context.Background(), ctxpkg.NewSession("q-tool-boundary", dir), qualitySpec(true), nil)
	if err != nil || res.Status != BudgetExhausted || res.StopCode != "tool_limit" || res.Usage.ToolCalls != 2 || res.Usage.ModelCalls > 3 || calls > 3 || res.PartialReport == nil {
		t.Fatalf("最后工具准入越界或误报成功：%+v err=%v calls=%d", res, err, calls)
	}
	if _, err := os.Stat(filepath.Join(dir, "汇总.md")); !os.IsNotExist(err) {
		t.Fatal("没有工具额度仍写入正式产物")
	}
}

func TestQualityChangingOnlyChecksCannotReuseOriginalReview(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "score/10*25\n")
	calls := 0
	var draft QualityDraft
	model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		switch modelStage(ctx) {
		case "quality_review":
			raw, _ := json.Marshal(QualityReview{SchemaVersion: 1, Findings: []QualityReviewFinding{{FindingID: "F1", Verdict: "supported", Reason: "给出的原式及样本计算成立"}}})
			return reply(string(raw))
		case "quality_revision":
			changed := draft.Checks[0]
			changed.Expression = "score/10*100"
			changed.Premise = "proposed"
			changed.Cases = []quality.Case{{Name: "另一个目标", Inputs: map[string]string{"score": "10"}, Expected: "100"}}
			// 故意保持Finding字节完全一致，只替换没有送审过的检查语义。
			return qualityCall("write_report", "publish", QualitySubmission{DraftID: currentRun(ctx).state.Quality.DraftID, Corrections: []QualityCorrection{{Operation: "replace", FindingID: "F1", Finding: &draft.Findings[0], Checks: []quality.Check{changed}}}})
		}
		if calls == 1 {
			return modelCalls(readCall("r", "source.md"))
		}
		e := analysisIntegrationEvidence(ctx)[0]
		draft = qualityTestDraft(e)
		draft.Findings[0].Statement = "按所列公式和样本进行静态推导"
		draft.Checks = []quality.Check{{ID: "C1", FindingID: "F1", Kind: "numeric", Subject: quality.Selector{EvidenceIDs: []string{e.ID}, ByteEnd: int64(len(e.Source))}, Expression: "score/10*25", Premise: "translated", Bindings: map[string]quality.Binding{"score": {Kind: "test_input"}}, Cases: []quality.Case{{Name: "原样本", Inputs: map[string]string{"score": "10"}, Expected: "25"}}}}
		return qualityCall("verify_findings", "v", draft)
	})
	res, err := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace}).Run(context.Background(), ctxpkg.NewSession("q-check-change", dir), qualitySpec(true), nil)
	if err != nil || res.Status != Blocked || res.StopCode != "quality_not_ready" || calls != 4 {
		t.Fatalf("新检查语义继承了旧复核：%+v calls=%d err=%v", res, calls, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "汇总.md")); !os.IsNotExist(err) {
		t.Fatal("未经复核的新检查内容被发布")
	}
}

func TestQualityLiteralFactsDoNotCarryUnreviewedContextClaims(t *testing.T) {
	dir := t.TempDir()
	writeFixture(t, dir, "source.md", "代码仅检查非空。\n")
	calls := 0
	model := reportModel(func(ctx context.Context, _ []schema.Message, _ []schema.ToolDefinition) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return modelCalls(readCall("r", "source.md"))
		}
		if modelStage(ctx) == "quality_review" {
			t.Fatal("测试应走逐字材料事实的程序范围投影")
		}
		if modelStage(ctx) == "quality_revision" {
			return qualityCall("write_report", "p", QualitySubmission{DraftID: currentRun(ctx).state.Quality.DraftID, Corrections: []QualityCorrection{}})
		}
		d := qualityTestDraft(analysisIntegrationEvidence(ctx)[0])
		d.Findings[0].Context = "全部URL已经验证合法。"
		d.Findings[0].Limitations = "所有脚本已执行通过。"
		d.Findings[0].Conditions.DefaultBehavior = "缺字段默认0。"
		return qualityCall("verify_findings", "v", d)
	})
	res, err := NewAgentEngine(model, NewWorkspaceRegistry(dir), EngineOptions{Mode: tools.ModeWorkspace}).Run(context.Background(), ctxpkg.NewSession("q-context", dir), qualitySpec(true), nil)
	if err != nil || res.Status != NeedsVerification || calls != 3 {
		t.Fatalf("%+v calls=%d err=%v", res, calls, err)
	}
	body, err := os.ReadFile(filepath.Join(dir, "汇总.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, unsupported := range []string{"全部URL已经验证合法", "所有脚本已执行通过", "缺字段默认0"} {
		if strings.Contains(string(body), unsupported) {
			t.Fatalf("未经复核的Context语义进入正式正文：%s", body)
		}
	}
}
