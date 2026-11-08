package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ReadFeatureSpec 独立控制普通任务实验能力；协议4使不认识这些开关的旧引擎拒绝恢复。
// 完整有效配置随任务持久化，读取记忆的数据格式仍为1。
type ReadFeatureSpec struct {
	Version     int  `json:"version"`
	SourceViews bool `json:"source_views"`
	LocalChecks bool `json:"local_checks"`
	ClaimFocus  bool `json:"claim_focus"`
}

// JSON中的实验对象必须完整，不能把缺失或null悄悄解释为关闭。
// CLI及Go调用方先经Normalize规范化，编码后自然具有全部字段。
func (f *ReadFeatureSpec) UnmarshalJSON(raw []byte) error {
	var input struct {
		Version     *int  `json:"version"`
		SourceViews *bool `json:"source_views"`
		LocalChecks *bool `json:"local_checks"`
		ClaimFocus  *bool `json:"claim_focus"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return fmt.Errorf("读取实验JSON无效: %w", err)
	}
	if decoder.Decode(new(any)) != io.EOF || input.Version == nil || input.SourceViews == nil || input.LocalChecks == nil || input.ClaimFocus == nil {
		return errors.New("读取实验JSON必须包含version及source_views/local_checks/claim_focus三个非null布尔值")
	}
	*f = ReadFeatureSpec{Version: *input.Version, SourceViews: *input.SourceViews, LocalChecks: *input.LocalChecks, ClaimFocus: *input.ClaimFocus}
	return nil
}

func (f ReadFeatureSpec) any() bool { return f.SourceViews || f.LocalChecks || f.ClaimFocus }

// ParseReadFeatureSwitches 只解析显式参数；省略与显式off由调用方保留区别。
func ParseReadFeatureSwitches(values map[string]string) (*ReadFeatureSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	f := &ReadFeatureSpec{Version: 1}
	for name, value := range values {
		if value != "on" && value != "off" {
			return nil, fmt.Errorf("实验开关 %s 必须为 on 或 off", name)
		}
		enabled := value == "on"
		switch name {
		case "source_views":
			f.SourceViews = enabled
		case "local_checks":
			f.LocalChecks = enabled
		case "claim_focus":
			f.ClaimFocus = enabled
		default:
			return nil, fmt.Errorf("未知读取实验开关 %s", name)
		}
	}
	return f, nil
}

// NormalizeReadFeatures 仅用于新任务；恢复读取原策略，不重新规范化历史记录。
func NormalizeReadFeatures(spec *TaskSpec) error {
	if spec.Resume {
		return errors.New("恢复任务须沿用持久读取配置，不能重新规范化实验开关")
	}
	if spec.ReadContextVersion < 0 || spec.ReadContextVersion > 4 {
		return errors.New("读取上下文版本无效")
	}
	if spec.ReadFeatures == nil {
		if spec.ReadContextVersion == 0 {
			spec.ReadContextVersion = 2
		}
		return ValidateReadFeatures(*spec)
	}
	f := *spec.ReadFeatures
	if f.Version != 1 {
		return errors.New("读取实验配置仅支持version=1")
	}
	if spec.ReadContextVersion == 1 || spec.ReadContextVersion == 3 {
		return errors.New("历史读取策略不能同时指定新实验开关")
	}
	if !f.any() {
		spec.ReadContextVersion, spec.ReadFeatures = 2, nil
		return ValidateReadFeatures(*spec)
	}
	if spec.ReadContextVersion != 0 && spec.ReadContextVersion != 4 {
		return errors.New("开启读取实验须使用新任务协议4，不能覆盖显式旧策略")
	}
	spec.ReadContextVersion, spec.ReadFeatures = 4, &f
	return ValidateReadFeatures(*spec)
}

// ValidateReadFeatures 不填默认值，供恢复及只读Inspection共同失败关闭。
func ValidateReadFeatures(spec TaskSpec) error {
	if spec.ReadContextVersion < 0 || spec.ReadContextVersion > 4 {
		return errors.New("不支持的读取上下文版本")
	}
	if (spec.ReadContextVersion == 3 || spec.ReadContextVersion == 4) && (spec.Analysis || spec.Report != nil || spec.Quality != nil) {
		return errors.New("读取实验仅支持普通任务，不能组合分析、报告或质量模式")
	}
	if spec.ReadContextVersion == 4 {
		if spec.ReadFeatures == nil || spec.ReadFeatures.Version != 1 || !spec.ReadFeatures.any() {
			return errors.New("读取协议4必须包含有效version=1且至少一项开启的完整实验配置")
		}
	} else if spec.ReadFeatures != nil {
		return errors.New("历史读取策略不能携带新实验配置")
	}
	return nil
}

func (r *runState) readFeaturesV2() bool {
	return r != nil && r.state.Spec.ReadContextVersion == 4 && ValidateReadFeatures(r.state.Spec) == nil
}

func (r *runState) readFeatureEnabled(name string) bool {
	if r == nil {
		return false
	}
	spec := r.state.Spec
	legacy := spec.ReadContextVersion == 3 && ValidateReadFeatures(spec) == nil
	if !legacy && !r.readFeaturesV2() {
		return false
	}
	switch name {
	case "source_views":
		return legacy || spec.ReadFeatures.SourceViews
	case "local_checks":
		return legacy || spec.ReadFeatures.LocalChecks
	case "claim_focus":
		return legacy || spec.ReadFeatures.ClaimFocus
	}
	return false
}

func validateReadFeatureTransition(before, after taskState) error {
	if before.Spec.ReadContextVersion != after.Spec.ReadContextVersion {
		return errors.New("同一任务不能更换持久读取协议")
	}
	a, b := before.Spec.ReadFeatures, after.Spec.ReadFeatures
	if (a == nil) != (b == nil) || a != nil && *a != *b {
		return errors.New("同一任务不能更换持久读取实验配置")
	}
	return nil
}

func validateReadFeaturesResume(request TaskSpec, saved TaskSpec) error {
	if request.ReadContextVersion != 0 && request.ReadContextVersion != saved.ReadContextVersion {
		return errors.New("恢复时不能修改持久读取协议")
	}
	if request.ReadFeatures == nil {
		return nil
	}
	if saved.ReadContextVersion != 4 || saved.ReadFeatures == nil {
		return errors.New("历史任务恢复不能开启或指定新读取实验配置")
	}
	f := *request.ReadFeatures
	if f != *saved.ReadFeatures {
		return errors.New("恢复时不能修改持久读取实验配置")
	}
	return nil
}
