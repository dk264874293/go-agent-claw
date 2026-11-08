package observe

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSourceViewRangeRetainsExplicitZeroAndUnknown(t *testing.T) {
	start, end := int64(0), int64(12)
	p := cleanPayload(Payload{ByteStart: &start, ByteEnd: &end}, nil)
	start = 9
	raw, _ := json.Marshal(p)
	if !strings.Contains(string(raw), `"byte_start":0`) || !strings.Contains(string(raw), `"byte_end":12`) {
		t.Fatal(string(raw))
	}
	raw, _ = json.Marshal(cleanPayload(Payload{}, nil))
	if strings.Contains(string(raw), `"byte_start"`) || strings.Contains(string(raw), `"byte_end"`) {
		t.Fatalf("未知区间被写为零: %s", raw)
	}
}
