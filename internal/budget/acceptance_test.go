package budget

import "testing"

func TestAcceptancePerRequestRounding(t *testing.T) {
	p := Price{InputPerMillionMicros: 1, OutputPerMillionMicros: 1, Currency: "USD", Version: "验收版本"}
	for _, tc := range []struct{ input, output, want int64 }{{0, 0, 0}, {1, 0, 1}, {0, 1, 1}, {500000, 500000, 1}, {500001, 500000, 2}} {
		got, err := p.CostMicros(tc.input, tc.output)
		if err != nil || got != tc.want {
			t.Fatalf("按请求向上舍入: %+v got=%d err=%v", tc, got, err)
		}
	}
	first, _ := p.CostMicros(1, 0)
	second, _ := p.CostMicros(1, 0)
	if first+second != 2 {
		t.Fatal("跨请求舍入被合并")
	}
}
