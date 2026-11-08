package observe

import (
	"math"
	"os"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

var (
	apiSecret        = regexp.MustCompile(`(?i)\bsk-[a-z0-9_*\-]{5,}`)
	authSecret       = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[a-z0-9+/_.=\-]+`)
	assignmentSecret = regexp.MustCompile(`(?i)(["']?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|password|secret|authorization)["']?\s*[:=]\s*)["']?[^\s,"';}]+["']?`)
	urlSecret        = regexp.MustCompile(`(https?://)[^\s/@:]+:[^\s/@]+@`)
)

func knownSecrets() []string {
	var values []string
	for _, item := range os.Environ() {
		key, value, _ := strings.Cut(item, "=")
		key = strings.ToUpper(key)
		if len(value) >= 8 && (strings.Contains(key, "API_KEY") || strings.HasSuffix(key, "TOKEN") || strings.HasSuffix(key, "SECRET") || strings.HasSuffix(key, "PASSWORD")) {
			values = append(values, value)
		}
	}
	sort.Slice(values, func(i, j int) bool { return len(values[i]) > len(values[j]) })
	return values
}

// Redact 在发送到页面或写入新增观测流前处理已知凭据与常见凭据格式。
// 任意业务正文不属于观测白名单，不能依赖此函数猜测其中所有秘密。
func Redact(value string) string { return redact(value, knownSecrets()) }

// NewRedactor 为一批响应复用同一份凭据快照，避免逐字段扫描进程环境。
func NewRedactor() func(string) string {
	secrets := knownSecrets()
	return func(value string) string { return redact(value, secrets) }
}

// RedactPrefix 用于有界读取后被截断的文本，额外遮蔽末尾的已知凭据前缀。
// 直接截断再脱敏会使跨截断边界的凭据无法匹配完整值。
func RedactPrefix(value string) string {
	secrets := knownSecrets()
	for _, secret := range secrets {
		for n := min(len(secret)-1, len(value)); n > 0; n-- {
			if strings.HasSuffix(value, secret[:n]) {
				value = value[:len(value)-n] + "[REDACTED]"
				break
			}
		}
	}
	return redact(value, secrets)
}
func redact(value string, secrets []string) string {
	value = strings.ToValidUTF8(value, "�")
	for _, secret := range secrets {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	value = apiSecret.ReplaceAllString(value, "[REDACTED]")
	value = authSecret.ReplaceAllString(value, "[REDACTED]")
	value = assignmentSecret.ReplaceAllString(value, "${1}[REDACTED]")
	return urlSecret.ReplaceAllString(value, "${1}[REDACTED]@")
}
func bounded(value string, n int) string {
	if len(value) <= n {
		return value
	}
	value = value[:n]
	for !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
func cleanPayload(p Payload, secrets []string) Payload {
	if p.DeliveryReview != nil {
		copy := *p.DeliveryReview
		copy.Status = bounded(redact(copy.Status, secrets), 64)
		copy.AdmissionCount = max(0, copy.AdmissionCount)
		copy.Documents = max(0, copy.Documents)
		copy.Addressed = max(0, copy.Addressed)
		copy.NotRelevant = max(0, copy.NotRelevant)
		copy.Unverified = max(0, copy.Unverified)
		copy.Undeclared = max(0, copy.Undeclared)
		copy.ReadyToResolve = max(0, copy.ReadyToResolve)
		copy.Unresolved = max(0, copy.Unresolved)
		p.DeliveryReview = &copy
	}
	p.Request = cleanRequestSummary(p.Request, secrets)
	if p.ReadFeatures != nil {
		copy := *p.ReadFeatures
		copy.Contract = bounded(redact(copy.Contract, secrets), 64)
		p.ReadFeatures = &copy
	}
	for _, ref := range []**int64{&p.ByteStart, &p.ByteEnd, &p.DeliveryReviewPromptBytes, &p.FeaturePromptBytes, &p.ChecksRequested, &p.LocalCheckSchemaBytes, &p.SourceViewSchemaBytes} {
		if *ref != nil {
			value := **ref
			if value < 0 {
				*ref = nil
			} else {
				*ref = &value
			}
		}
	}
	p.UnprocessedReadCount = max(0, p.UnprocessedReadCount)
	p.FoldedSourceBytes = max(0, p.FoldedSourceBytes)
	p.OmittedNoteEntries = max(0, p.OmittedNoteEntries)
	p.NotesAccepted, p.NotesRejected, p.NotesMissing = max(0, p.NotesAccepted), max(0, p.NotesRejected), max(0, p.NotesMissing)
	p.NotesCarrierBytes = max(0, p.NotesCarrierBytes)
	p.NotesMetadataRemovedBytes, p.NotesMetadataRetainedBytes = max(0, p.NotesMetadataRemovedBytes), max(0, p.NotesMetadataRetainedBytes)
	p.NotesPreviouslyDelivered, p.NotesThisRequest = max(0, p.NotesPreviouslyDelivered), max(0, p.NotesThisRequest)
	p.NotesCandidates = max(0, p.NotesCandidates)
	p.NotesProcessedSources, p.NotesDeferredSources = max(0, p.NotesProcessedSources), max(0, p.NotesDeferredSources)
	p.NotesSubmitted, p.NotesResponseRejected, p.NotesDuplicates = max(0, p.NotesSubmitted), max(0, p.NotesResponseRejected), max(0, p.NotesDuplicates)
	v := reflect.ValueOf(&p).Elem()
	for i := 0; i < v.NumField(); i++ {
		if v.Field(i).Kind() == reflect.String {
			limit := 256
			switch v.Type().Field(i).Name {
			case "Goal", "Summary":
				limit = 512
			case "Path":
				limit = 1024
			}
			v.Field(i).SetString(bounded(redact(v.Field(i).String(), secrets), limit))
		}
	}
	if p.EncodedRequestBytes != nil {
		value := *p.EncodedRequestBytes
		if value < 0 {
			p.EncodedRequestBytes = nil
		} else {
			p.EncodedRequestBytes = &value
		}
	}
	if p.InputTokens != nil {
		value := *p.InputTokens
		if value < 0 {
			p.InputTokens = nil
		} else {
			p.InputTokens = &value
		}
	}
	if p.OutputTokens != nil {
		value := *p.OutputTokens
		if value < 0 {
			p.OutputTokens = nil
		} else {
			p.OutputTokens = &value
		}
	}
	for _, ref := range []**float64{&p.Cost, &p.InputPerMillion, &p.OutputPerMillion} {
		if *ref != nil {
			value := **ref
			if value < 0 || math.IsNaN(value) || math.IsInf(value, 0) {
				*ref = nil
			} else {
				*ref = &value
			}
		}
	}
	if p.ExpiresAt != nil {
		value := *p.ExpiresAt
		p.ExpiresAt = &value
	}
	return p
}
