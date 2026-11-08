package engine

// qualityFaithfulProjection 要求关键来源的完整依赖闭包均仍在本次笔记投影中。
// 泛摘要或只有 processed 声明不能授予折叠；有未解决项时保守保留原文。
func qualityFaithfulProjection(evidenceID string, reports []AnalysisReport) bool {
	found := false
	for _, report := range reports {
		if len(report.Unverified) > 0 || report.UnverifiedDropped > 0 {
			return false
		}
		for _, read := range report.DeferredReads {
			if read.EvidenceID == evidenceID {
				return false
			}
		}
		byID := map[string]AnalysisFinding{}
		for _, f := range report.Findings {
			byID[f.ID] = f
		}
		var valid func(string, map[string]bool) bool
		valid = func(id string, path map[string]bool) bool {
			f, ok := byID[id]
			if !ok || path[id] || f.Status != "evidence_valid" || f.Context == "" {
				return false
			}
			path[id] = true
			defer delete(path, id)
			for _, ref := range f.Evidence {
				if ref.Quote == "" || ref.StartLine < 1 || ref.EndLine < ref.StartLine {
					return false
				}
			}
			for _, dep := range f.DependsOn {
				if !valid(dep, path) {
					return false
				}
			}
			return len(f.Evidence) > 0 || len(f.DependsOn) > 0
		}
		for _, f := range report.Findings {
			for _, ref := range f.Evidence {
				if ref.EvidenceID == evidenceID {
					if !valid(f.ID, map[string]bool{}) {
						return false
					}
					found = true
				}
			}
		}
	}
	return found
}
