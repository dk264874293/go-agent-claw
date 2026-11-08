package quality

import (
	"fmt"
	"strings"
	"testing"
)

func splitSources(raw string, ranges ...[2]int) (Selector, Lookup, map[string]Source) {
	parts := map[string]Source{}
	ids := []string{}
	for i, span := range ranges {
		id := fmt.Sprintf("E%d", i+1)
		ids = append(ids, id)
		parts[id] = Source{ID: id, Path: "资料.md", Version: strings.Repeat("a", 64), ByteStart: int64(span[0]), ByteEnd: int64(span[1]), TotalBytes: int64(len(raw)), FirstLine: 1 + strings.Count(raw[:span[0]], "\n"), Content: []byte(raw[span[0]:span[1]])}
	}
	return Selector{EvidenceIDs: ids, ByteEnd: int64(len(raw))}, func(id string) (Source, error) {
		s, ok := parts[id]
		if !ok {
			return Source{}, fmt.Errorf("未读来源")
		}
		return s, nil
	}, parts
}
func TestResolveContiguousOverlapAndActualBytes(t *testing.T) {
	raw := "{\r\n  \"中文\": 5,\n  \"null\": null\n}"
	for _, ranges := range [][][2]int{{{0, 13}, {13, len(raw)}}, {{0, 18}, {12, len(raw)}}, {{12, len(raw)}, {0, 18}}} {
		s, lookup, _ := splitSources(raw, ranges...)
		resolved, err := Resolve(s, lookup)
		if err != nil || string(resolved.Content) != raw || resolved.Path != "资料.md" || resolved.ByteEnd != int64(len(raw)) || len(resolved.SHA256) != 64 || !resolved.StartsLine || !resolved.EndsLine {
			t.Fatalf("%+v %v", resolved, err)
		}
		c := Check{ID: "J1", FindingID: "F1", Kind: "json", Subject: s, Assertions: []Assertion{{Op: "equals", Pointer: "/中文", Expected: []byte("5")}}}
		r := Run([]Check{c}, lookup)[0]
		if r.Result != "pass" {
			t.Fatal(r)
		}
	}
}
func TestResolveRejectsGapOverlapVersionAndUnreadSelection(t *testing.T) {
	raw := "abcdefghij"
	for _, scenario := range []string{"gap", "overlap", "version", "path", "size", "unread", "UTF8"} {
		t.Run(scenario, func(t *testing.T) {
			s, lookup, parts := splitSources(raw, [2]int{0, 6}, [2]int{4, 10})
			b := parts["E2"]
			switch scenario {
			case "gap":
				b.ByteStart = 7
				b.Content = []byte(raw[7:])
				parts["E2"] = b
			case "overlap":
				b.Content = []byte("xxghij")
				parts["E2"] = b
			case "version":
				b.Version = "new"
				parts["E2"] = b
			case "path":
				b.Path = "另一份资料.md"
				parts["E2"] = b
			case "size":
				b.Content = []byte("short")
				parts["E2"] = b
			case "unread":
				s.ByteEnd = 11
			case "UTF8":
				b.Content = []byte{'e', 'f', 'g', 'h', 'i', 0xff}
				parts["E2"] = b
			}
			_, err := Resolve(s, lookup)
			if err == nil || !sourceUnavailable(err) {
				t.Fatalf("%s 未阻断或混为checker错误：%v", scenario, err)
			}
		})
	}
}
func TestResolveDoesNotGuessMissingLineBoundary(t *testing.T) {
	raw := "prefix fragment end\nnext"
	s, lookup, parts := splitSources(raw, [2]int{7, 15})
	s.ByteStart = 7
	s.ByteEnd = 15
	s.RequireCompleteLine = true
	if _, err := Resolve(s, lookup); err == nil || !sourceUnavailable(err) {
		t.Fatalf("旧缺失边界不应证明完整行：%v", err)
	}
	b := parts["E1"]
	b.Boundary = &Boundary{StartsLine: false, EndsLine: false}
	parts["E1"] = b
	if _, err := Resolve(s, lookup); err == nil {
		t.Fatal("半行不能证明未读部分")
	}
	// 不要求完整行时合法片段仍可选择；不是EOF不能据此拒绝。
	s.RequireCompleteLine = false
	if _, err := Resolve(s, lookup); err != nil {
		t.Fatal(err)
	}
	// 来源自身包含换行的直接字节依据可证明一整行，不猜文件mtime或输出头。
	s, lookup, _ = splitSources("first\nsecond\r\nthird", [2]int{0, 19})
	s.ByteStart = 6
	s.ByteEnd = 14
	s.RequireCompleteLine = true
	resolved, err := Resolve(s, lookup)
	if err != nil || resolved.FirstLine != 2 || resolved.LastLine != 2 || string(resolved.Content) != "second\r\n" {
		t.Fatalf("%+v %v", resolved, err)
	}
}
func TestResolveUTF8SelectionAndResourceLimits(t *testing.T) {
	s, lookup := material("甲乙\n")
	s.ByteStart = 1
	s.ByteEnd = 3
	if _, err := Resolve(s, lookup); err == nil || !sourceUnavailable(err) {
		t.Fatal("不能接受中文半字符")
	}
	s.ByteStart = 0
	s.ByteEnd = MaxJSONBytes + 1
	if _, err := Resolve(s, lookup); err == nil || sourceUnavailable(err) {
		t.Fatal("过大选择应是输入错误")
	}
	s, lookup = material("{} ")
	s.EvidenceIDs = append(s.EvidenceIDs, s.EvidenceIDs[0])
	if _, err := Resolve(s, lookup); err == nil || sourceUnavailable(err) {
		t.Fatal("重复来源应是输入错误")
	}
}
