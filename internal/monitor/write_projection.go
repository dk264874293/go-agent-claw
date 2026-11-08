package monitor

import (
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/dk264874293/go-agent-claw/internal/engine"
	"github.com/dk264874293/go-agent-claw/internal/observe"
)

func (x *Index) projectReadMemory(t *Task, memory *engine.ReadMemoryState) {
	if memory == nil {
		return
	}
	view := &ReadMemory{Version: memory.Version, EvidenceCount: len(memory.Evidence), Invalidations: len(memory.Invalidations)}
	for _, report := range memory.Notes {
		view.NoteCount += len(report.Findings)
	}
	for _, summaries := range memory.Summaries {
		view.NoteCount += len(summaries)
	}
	for _, ids := range memory.ProcessedReads {
		view.Processed += len(ids)
	}
	t.ReadMemory = view
}

func (x *Index) projectReport(t *Task, report *engine.ReportDelivery) {
	if report == nil {
		return
	}
	// 先复制，脱敏不能反向修改引擎的只读状态。
	raw, _ := json.Marshal(report)
	var copy engine.ReportDelivery
	if json.Unmarshal(raw, &copy) != nil {
		return
	}
	copy.Text = observe.Redact(copy.Text)
	copy.TargetPath = observe.Redact(copy.TargetPath)
	for i := range copy.Issues {
		copy.Issues[i] = observe.Redact(copy.Issues[i])
	}
	t.Report = &copy
}

func (x *Index) projectQuality(t *Task, result *engine.QualityDelivery) {
	if result == nil {
		return
	}
	view := &Quality{
		Version: result.Version, DraftID: observe.Redact(result.DraftID), Status: observe.Redact(result.Status),
		CheckResult: observe.Redact(result.CheckResult), ReviewStatus: observe.Redact(result.ReviewStatus), RevisionStatus: observe.Redact(result.RevisionStatus),
	}
	for i, issue := range result.Unresolved {
		if i >= 192 {
			break
		}
		view.Unresolved = append(view.Unresolved, clip(observe.Redact(issue), 4000))
	}
	t.Quality = view
}

func validArtifactPath(path string) bool {
	if !filepath.IsLocal(path) || strings.ContainsRune(path, 0) {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return false
		}
	}
	return true
}

func (x *Index) projectArtifact(t *Task, root string, ref engine.ArtifactRef) {
	version, err := hex.DecodeString(ref.Version)
	if err != nil || len(version) != 32 || strings.ToLower(ref.Version) != ref.Version || !validArtifactPath(ref.Path) {
		return
	}
	path := filepath.ToSlash(filepath.Clean(ref.Path))
	id := stableID(root + "\x00" + path + "\x00" + ref.Version)
	label := "确认产物 " + path
	x.evidence[id] = evidenceSource{root: root, relative: path, label: label, expectedVersion: ref.Version}
	t.Artifacts = append(t.Artifacts, Artifact{ActionID: ref.ActionID, Path: observe.Redact(path), Version: ref.Version, EvidenceID: id})
	if !containsEvidence(t.Evidence, id) {
		t.Evidence = append(t.Evidence, Evidence{ID: id, Label: observe.Redact(label)})
	}
}
