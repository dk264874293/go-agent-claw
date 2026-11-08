package quality

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func material(raw string) (Selector, Lookup) {
	selector := Selector{EvidenceIDs: []string{"E1"}, ByteEnd: int64(len(raw))}
	source := Source{ID: "E1", Path: "资料.md", Version: fmt.Sprintf("%x", sha256.Sum256([]byte(raw))), ByteEnd: int64(len(raw)), TotalBytes: int64(len(raw)), FirstLine: 1, LastLine: 1, Content: []byte(raw), Boundary: &Boundary{true, true}}
	return selector, func(id string) (Source, error) {
		if id != "E1" {
			return Source{}, errors.New("来源不存在")
		}
		return source, nil
	}
}
func numeric(expression string) (Check, Lookup) {
	s, lookup := material(expression)
	return Check{ID: "C1", FindingID: "F1", Kind: "numeric", Subject: s, Expression: expression, Premise: "source_arithmetic", Bindings: map[string]Binding{"score": {Kind: "test_input"}}, Cases: []Case{{Name: "上界", Inputs: map[string]string{"score": "10"}, Expected: "25"}}}, lookup
}
func TestArithmeticDetectsSuggestedMissingFactor(t *testing.T) {
	c, lookup := numeric("(score-1)/9")
	r := Run([]Check{c}, lookup)[0]
	if r.Result != "fail" || r.PremiseResult != "pass" || r.Cases[0].Actual != "1" {
		t.Fatalf("漏乘系数须产生反例：%+v", r)
	}
	if !strings.Contains(RenderReceipt(r), "实际 1，断言 25") || !strings.Contains(r.Formula, "/") {
		t.Fatal(RenderReceipt(r))
	}
	c, lookup = numeric("(score-1)/9*25")
	c.Cases = append(c.Cases, Case{Name: "下界", Inputs: map[string]string{"score": "1"}, Expected: "0"}, Case{Name: "默认值", Inputs: map[string]string{"score": "5"}, Expected: "11.111111"})
	r = Run([]Check{c}, lookup)[0]
	if r.Result != "fail" || r.Cases[2].Actual != "100/9" {
		t.Fatalf("精确有理数不能隐式容差：%+v", r)
	}
}
func TestArithmeticDefaultAndSum(t *testing.T) {
	c, lookup := numeric("score/10*25")
	c.Cases = []Case{{Name: "缺省样本", Inputs: map[string]string{"score": "5"}, Expected: "12.5"}}
	r := Run([]Check{c}, lookup)[0]
	if r.Result != "pass" || r.Cases[0].Actual != "25/2" {
		t.Fatal(r)
	}
	c.Subject, lookup = material("sum(25, 25, 25, 22.5)")
	c.Expression = "sum(25, 25, 25, 22.5)"
	c.Bindings = nil
	c.Cases = []Case{{Expected: "84.5"}}
	r = Run([]Check{c}, lookup)[0]
	if r.Result != "fail" || r.Cases[0].Actual != "195/2" {
		t.Fatal(r)
	}
}
func TestArithmeticPremiseIsNotCodeExecution(t *testing.T) {
	c, lookup := numeric("score/10*25")
	c.Subject, lookup = material(`data.get("score", 5) / 10 * 25`)
	r := Run([]Check{c}, lookup)[0]
	if r.Result != "pass" || r.PremiseResult != "not_run" {
		t.Fatal(r)
	}
	c.Premise = "translated"
	r = Run([]Check{c}, lookup)[0]
	if r.PremiseResult != "not_run" {
		t.Fatal(r)
	}
	// 选择到语法完整但截掉乘数的前半行，不能给来源前提授予pass。
	c, lookup = numeric("score/10*25")
	c.Subject, lookup = material("score/10*25*2\n")
	c.Subject.ByteEnd = int64(len(c.Expression))
	r = Run([]Check{c}, lookup)[0]
	if r.Result != "pass" || r.PremiseResult != "not_run" {
		t.Fatal(r)
	}
}
func TestArithmeticSourceValueCannotBeForgedOrOverridden(t *testing.T) {
	s, lookup := material("84.5")
	c := Check{ID: "C1", FindingID: "F1", Kind: "numeric", Subject: s, Expression: "value", Bindings: map[string]Binding{"value": {Kind: "source_value", Source: &s, Value: "97.5"}}, Cases: []Case{{Expected: "97.5"}}}
	r := Run([]Check{c}, lookup)[0]
	if r.Result != "error" || !strings.Contains(r.Reason, "不一致") {
		t.Fatal(r)
	}
	b := c.Bindings["value"]
	b.Value = ""
	c.Bindings["value"] = b
	c.Cases[0].Inputs = map[string]string{"value": "97.5"}
	r = Run([]Check{c}, lookup)[0]
	if r.Result != "error" || !strings.Contains(r.Reason, "不能覆盖") {
		t.Fatal(r)
	}
	c.Cases[0].Inputs = nil
	c.Cases[0].Expected = "84.5"
	r = Run([]Check{c}, lookup)[0]
	if r.Result != "pass" || len(r.Sources) != 2 || r.Sources[1].SHA256 == "" {
		t.Fatal(r)
	}
	partial := s
	partial.ByteStart = 1
	partial.ByteEnd = 2
	b.Source = &partial
	c.Bindings["value"] = b
	r = Run([]Check{c}, lookup)[0]
	if r.Result != "not_run" || !strings.Contains(r.Reason, "token") {
		t.Fatal(r)
	}
	// 尾部空格不能掩盖左侧token被切断。
	s, lookup = material("84 ")
	partial = s
	partial.ByteStart = 1
	b.Source = &partial
	c.Subject = s
	c.Bindings["value"] = b
	r = Run([]Check{c}, lookup)[0]
	if r.Result != "not_run" {
		t.Fatal(r)
	}
}
func TestArithmeticSourceJSONPointer(t *testing.T) {
	s, lookup := material(`{"score":5,"nested":{"score":9}}`)
	c := Check{ID: "C1", FindingID: "F1", Kind: "numeric", Subject: s, Expression: "score / 10 * 25", Bindings: map[string]Binding{"score": {Kind: "source_value", Source: &s, Pointer: "/score"}}, Cases: []Case{{Expected: "12.5"}}}
	r := Run([]Check{c}, lookup)[0]
	if r.Result != "pass" || r.PremiseResult != "not_run" {
		t.Fatal(r)
	}
	b := c.Bindings["score"]
	b.Pointer = "/nested/score"
	c.Bindings["score"] = b
	r = Run([]Check{c}, lookup)[0]
	if r.Result != "fail" || r.Cases[0].Actual != "45/2" {
		t.Fatal(r)
	}
}

func TestArithmeticSourceValueRequiresProvenTokenBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, raw              string
		start, end             int
		selectStart, selectEnd int64
		want                   string
	}{
		{name: "左端数字被截断", raw: "84.5", start: 1, end: 4, selectStart: 1, selectEnd: 4, want: "not_run"},
		{name: "右端数字被截断", raw: "84.5", start: 0, end: 2, selectStart: 0, selectEnd: 2, want: "not_run"},
		{name: "负号未读取", raw: "-5", start: 1, end: 2, selectStart: 1, selectEnd: 2, want: "not_run"},
		{name: "未知边界包住的内部空白可作依据", raw: "前缀 4.5 尾部", start: 6, end: 11, selectStart: 6, selectEnd: 11, want: "pass"},
		{name: "完整相邻分隔符可作依据", raw: "=4.5;", start: 0, end: 5, selectStart: 1, selectEnd: 4, want: "pass"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			formula := []byte("value")
			formulaSource := Source{ID: "F", Path: "formula.txt", Version: fmt.Sprintf("%x", sha256.Sum256(formula)), ByteEnd: int64(len(formula)), TotalBytes: int64(len(formula)), FirstLine: 1, Content: formula, Boundary: &Boundary{true, true}}
			numberSource := Source{ID: "N", Path: "numbers.txt", Version: fmt.Sprintf("%x", sha256.Sum256([]byte(tc.raw))), ByteStart: int64(tc.start), ByteEnd: int64(tc.end), TotalBytes: int64(len(tc.raw)), FirstLine: 1, Content: []byte(tc.raw[tc.start:tc.end]), Boundary: &Boundary{false, false}}
			lookup := func(id string) (Source, error) {
				if id == "F" {
					return formulaSource, nil
				}
				return numberSource, nil
			}
			sel := Selector{EvidenceIDs: []string{"N"}, ByteStart: tc.selectStart, ByteEnd: tc.selectEnd}
			expected := strings.TrimSpace(tc.raw[tc.selectStart:tc.selectEnd])
			check := Check{ID: "C", FindingID: "F1", Kind: "numeric", Subject: Selector{EvidenceIDs: []string{"F"}, ByteEnd: int64(len(formula))}, Expression: "value", Premise: "source_arithmetic", Bindings: map[string]Binding{"value": {Kind: "source_value", Source: &sel}}, Cases: []Case{{Expected: expected}}}
			r := Run([]Check{check}, lookup)[0]
			if r.Result != tc.want {
				t.Fatalf("未知邻字节被当成分隔符：%+v", r)
			}
		})
	}
}
func TestArithmeticRejectsUnsupportedAndLimits(t *testing.T) {
	for _, formula := range []string{"1/0", "pow(2,3)", "1e10", "x.attr", strings.Repeat("1+", 128) + "1", strings.Repeat("(", 18) + "1" + strings.Repeat(")", 18), strings.Repeat("9", 129)} {
		s, lookup := material(formula)
		c := Check{ID: "C1", FindingID: "F1", Kind: "numeric", Subject: s, Expression: formula, Cases: []Case{{Expected: "1"}}}
		r := Run([]Check{c}, lookup)[0]
		if r.Result != "error" {
			t.Fatalf("%s => %+v", formula, r)
		}
	}
	// 短字面量连乘也必须在4096位中间结果停止。
	formula := strings.Repeat(strings.Repeat("9", 128)+"*", 11) + strings.Repeat("9", 128)
	s, lookup := material(formula)
	c := Check{ID: "C1", FindingID: "F1", Kind: "numeric", Subject: s, Expression: formula, Cases: []Case{{Expected: "0"}}}
	r := Run([]Check{c}, lookup)[0]
	if r.Result != "error" || !strings.Contains(r.Reason, "4096") {
		t.Fatal(r)
	}
}
func jsonCheck(raw string, assertions ...Assertion) (Check, Lookup) {
	s, l := material(raw)
	return Check{ID: "J1", FindingID: "F1", Kind: "json", Subject: s, Assertions: assertions}, l
}
func TestJSONMissingDistinctFromFalseValues(t *testing.T) {
	for _, value := range []string{"null", "false", "0", `""`} {
		c, l := jsonCheck(`{"score":`+value+`}`, Assertion{Op: "exists", Pointer: "/score"}, Assertion{Op: "default", Pointer: "/score", Default: json.RawMessage("5"), Expected: json.RawMessage(value)}, Assertion{Op: "equals", Pointer: "/score", Expected: json.RawMessage(value)})
		r := Run([]Check{c}, l)[0]
		if r.Result != "pass" || r.PremiseResult != "pass" {
			t.Fatal(r)
		}
		c.Assertions = []Assertion{{Op: "default", Pointer: "/score", Default: json.RawMessage("5"), Expected: json.RawMessage("5")}}
		if Run([]Check{c}, l)[0].Result != "fail" {
			t.Fatal("不能将非缺失值换为默认值")
		}
	}
	c, l := jsonCheck(`{}`, Assertion{Op: "absent", Pointer: "/score"}, Assertion{Op: "default", Pointer: "/score", Default: json.RawMessage("5"), Expected: json.RawMessage("5")})
	r := Run([]Check{c}, l)[0]
	if r.Result != "pass" || r.Assertions[1].Present {
		t.Fatal(r)
	}
}
func TestJSONPointerPreciseObjectAndEscaping(t *testing.T) {
	c, l := jsonCheck(`{"a":{"score":5},"b":{},"a/b":{"~key":null},"arr":[1,2]}`, Assertion{Op: "equals", Pointer: "/a/score", Expected: json.RawMessage("5.0")}, Assertion{Op: "absent", Pointer: "/b/score"}, Assertion{Op: "type", Pointer: "/a~1b/~0key", Type: "null"}, Assertion{Op: "equals", Pointer: "/arr/1", Expected: json.RawMessage("2")})
	r := Run([]Check{c}, l)[0]
	if r.Result != "pass" {
		t.Fatal(r)
	}
	if !strings.Contains(r.Scope, "仅所选") {
		t.Fatal("证明范围必须限定对象")
	}
	for _, path := range []string{"/bad~2", "/missing/bad~2", "arr/1", "/arr/01"} {
		c.Assertions = []Assertion{{Op: "exists", Pointer: path}}
		r = Run([]Check{c}, l)[0]
		if r.Result != "error" {
			t.Fatalf("非法Pointer %s: %+v", path, r)
		}
	}
	c.Assertions = []Assertion{{Op: "equals", Pointer: "/a/score", Expected: json.RawMessage(`"5"`)}}
	if Run([]Check{c}, l)[0].Result != "fail" {
		t.Fatal("不能将字符串隐式转换为数字")
	}
}
func TestJSONStrictDuplicateDepthAndTrailing(t *testing.T) {
	for _, raw := range []string{`{"key":1,"\u006bey":2}`, `{"a":{"k":null,"k":1}}`, `{"x":1,}`, `{"x":1} {}`, `{"x":1}//注释`, `[1,2]`, `{"x":NaN}`, `{"x":1e99999999}`, strings.Repeat(`{"x":`, 33) + "1" + strings.Repeat("}", 33)} {
		c, l := jsonCheck(raw, Assertion{Op: "exists", Pointer: "/x"})
		r := Run([]Check{c}, l)[0]
		if r.Result != "error" {
			t.Fatalf("%s => %+v", raw, r)
		}
	}
}
func TestUnavailableSourcesAndCheckerInputSeparate(t *testing.T) {
	c, _ := jsonCheck(`{"a":1}`, Assertion{Op: "exists", Pointer: "/a"})
	r := Run([]Check{c}, func(string) (Source, error) { return Source{}, errors.New("版本陈旧或权限失效") })[0]
	if r.Result != "not_run" || r.PremiseResult != "not_run" {
		t.Fatal(r)
	}
	c.Subject.ByteEnd = 0
	r = Run([]Check{c}, nil)[0]
	if r.Result != "error" {
		t.Fatal(r)
	}
	checks := make([]Check, 65)
	r = Run(checks, nil)[0]
	if r.Result != "error" {
		t.Fatal(r)
	}
}
