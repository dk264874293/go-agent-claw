package engine

import (
	"context"
	"encoding/json"
	"time"

	"github.com/dk264874293/go-agent-claw/internal/observe"
	"github.com/dk264874293/go-agent-claw/internal/requestdiag"
	"github.com/dk264874293/go-agent-claw/internal/schema"
)

func diagnosticIdentity(ids observe.IdentityInfo) requestdiag.Identity {
	return requestdiag.Identity{ObservationAttemptID: ids.AttemptID, WorkspaceID: ids.WorkspaceID, TaskID: ids.TaskID, AgentID: ids.AgentID, ParentAgentID: ids.ParentAgentID, ModelCallID: ids.ModelCallID}
}

// StartRequestDiagnostics 由入口调用；独立材料的ID不会映射成普通工具证据。
func StartRequestDiagnostics(ctx context.Context, config requestdiag.Config) (context.Context, *requestdiag.Attempt) {
	base := observe.Identity(ctx)
	return requestdiag.Start(ctx, config, diagnosticIdentity(base), func(event requestdiag.Event) {
		ids := base
		ids.TaskID, ids.AgentID, ids.ModelCallID = event.Identity.TaskID, event.Identity.AgentID, event.Identity.ModelCallID
		ids.ParentAgentID = event.Identity.ParentAgentID
		request := &observe.RequestSummary{}
		payload := observe.Payload{Request: request}
		if m := event.Metadata; m != nil {
			request.Provider, request.Protocol, request.EndpointID, request.Model = m.Provider, m.Protocol, m.EndpointID, m.Model
			request.Parameters = map[string]observe.ParameterValue{}
			for key, p := range m.Parameters {
				request.Parameters[key] = observe.ParameterValue{State: p.Presence, Value: append(json.RawMessage(nil), p.Value...)}
			}
			for _, field := range []struct{ parent, name, value string }{
				{"tool_choice", "tool_choice_mode", m.ToolChoiceMode},
				{"tool_choice", "tool_choice_name", m.ToolChoiceName},
				{"response_format", "response_format_type", m.ResponseFormatType},
				{"response_format", "response_format_schema_fingerprint", m.ResponseFormatSchemaFingerprint},
				{"reasoning", "reasoning_effort", m.ReasoningEffort},
			} {
				p := observe.ParameterValue{State: m.Parameters[field.parent].Presence}
				if field.parent == "response_format" && p.State == "absent" && m.Parameters["text"].Presence != "absent" {
					p.State = m.Parameters["text"].Presence
				}
				if field.name == "reasoning_effort" && m.Parameters["reasoning_effort"].Presence != "absent" {
					p = request.Parameters[field.name]
				}
				if field.value != "" {
					p.State = "value"
					p.Value, _ = json.Marshal(field.value)
				} else if p.State == "value" {
					p.State = "unknown"
				}
				request.Parameters[field.name] = p
			}
			if m.CaptureState == "complete" {
				inputCount := m.InputCount
				request.InputCount, request.RoleCounts = &inputCount, m.RoleCounts
			}
			request.ToolNames = append([]string(nil), m.ToolNames...)
			request.SchemaFingerprint, request.RequestFingerprint = m.SchemaFingerprint, m.Fingerprint
			request.HTTPStatus, request.ServiceRequestID = m.HTTPStatus, stringOrEmpty(m.RequestID)
			request.PreviousModelCallID = m.PreviousSuccessfulCallID
			request.ManifestID, request.CaptureMode = m.ManifestID, m.CaptureMode
			request.CaptureStatus, request.CaptureReason = m.CaptureState, m.CaptureReason
			request.OmittedItems = m.OmittedItems
			if m.PreviousSuccessfulCallID != "" {
				added, removed, changed := len(m.AddedPaths), len(m.RemovedPaths), len(m.ChangedPaths)
				request.AddedInputs, request.RemovedInputs, request.ChangedInputs = &added, &removed, &changed
				changedSchema, complete := m.SchemaChanged, m.DiffComplete
				request.SchemaChanged, request.DiffComplete = &changedSchema, &complete
				for _, name := range m.ChangedParameters {
					if name == "text" {
						name = "response_format"
					}
					request.ChangedParameters = append(request.ChangedParameters, name)
				}
			}
			payload.Model, payload.HTTPAttempt, payload.EncodedRequestBytes = m.Model, int64(m.HTTPAttempt), m.RequestBytes
			payload.DurationMS, payload.ErrorClass = m.DurationMS, m.Error.Kind
			if m.Error.Kind != "" || m.Error.HTTPStatus != nil {
				request.Error = &observe.RequestErrorSummary{HTTPStatus: m.Error.HTTPStatus, Code: diagnosticErrorField(m.Error, "code", m.Error.Code), Type: diagnosticErrorField(m.Error, "type", m.Error.Type), Param: diagnosticErrorField(m.Error, "param", m.Error.Param), RequestID: diagnosticErrorField(m.Error, "request_id", m.Error.RequestID), Summary: m.Error.SafeSummary}
			}
		}
		if d := event.Diagnostic; d != nil {
			request.DiagnosticID, request.CaptureMode, request.CaptureStatus, request.CaptureReason = d.ID, d.Mode, d.State, d.Reason
			complete, redacted, previous, saved := d.Complete, d.Redacted, d.PreviousAvailable, d.SavedBytes
			request.CaptureComplete, request.CaptureRedacted, request.PreviousAvailable = &complete, &redacted, &previous
			request.OriginalBytes, request.SavedBytes = d.OriginalBytes, &saved
		}
		observe.EmitWithIdentity(ctx, ids, event.Kind, payload)
	})
}

// CloseRequestDiagnostics 给两条旁路记录链共享刷新期限，先提交诊断完成状态。
func CloseRequestDiagnostics(diagnostics *requestdiag.Attempt, observations *observe.Attempt) {
	deadline := time.Now().Add(time.Second)
	if diagnostics != nil {
		diagnostics.CloseWithin(time.Until(deadline))
	}
	if observations != nil {
		observations.CloseWithin(time.Until(deadline))
	}
}

func stringOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func diagnosticField(p *string) observe.ParameterValue {
	if p == nil {
		return observe.ParameterValue{State: "unknown"}
	}
	b, _ := json.Marshal(*p)
	return observe.ParameterValue{State: "value", Value: b}
}

func diagnosticErrorField(info requestdiag.ErrorInfo, key string, fallback *string) observe.ParameterValue {
	if state := info.FieldStates[key]; state != "" {
		return observe.ParameterValue{State: state, Value: append(json.RawMessage(nil), info.FieldValues[key]...)}
	}
	return diagnosticField(fallback)
}

// requestOrigins 只用结构化身份和实际投影标记，不解析工具正文元数据头。
func (r *runState) requestOrigins(messages []schema.Message) []requestdiag.Origin {
	r.mu.Lock()
	sources := make(map[string]AnalysisEvidence, len(r.analysisRuntime.sources))
	for id, e := range r.analysisRuntime.sources {
		sources[id] = e
	}
	r.mu.Unlock()
	result := make([]requestdiag.Origin, 0, len(messages))
	for i, m := range messages {
		o := requestdiag.Origin{InputIndex: i, Representation: m.RequestRepresentation, SourceKind: string(m.Role), EvidenceID: m.ReadEvidenceID, ActionID: m.ReadActionID, ToolCallID: m.ToolCallID}
		if e, ok := sources[m.ReadEvidenceID]; ok {
			o.SourceKind, o.ActionID, o.Version = "evidence", e.ActionID, e.Receipt.Version
			o.ByteStart, o.ByteEnd = e.Receipt.ByteStart, e.Receipt.ByteEnd
			if o.Representation == "" {
				o.Representation = "source"
			}
		}
		if o.Representation == "" {
			switch {
			case m.Role == schema.RoleSystem:
				o.Representation = "system_generated"
			case m.ReadEvidenceID != "":
				o.Representation = "unknown"
			case m.ToolCallID != "":
				o.Representation = "tool_result"
			case m.Role == schema.RoleAssistant:
				o.Representation = "model_response"
			default:
				o.Representation = "user_input"
			}
		}
		result = append(result, o)
	}
	return result
}
