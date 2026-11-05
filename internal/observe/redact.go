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
