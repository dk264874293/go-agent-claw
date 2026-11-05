package engine

import (
	"encoding/json"
	"github.com/dk264874293/go-agent-claw/internal/taskstore"
	"testing"
)

func TestInspectionDoesNotReuseOldTerminal(t *testing.T) {
	v := Inspection{}
	s := taskState{Version: 1, LastResult: &RunResult{Status: Answered, FinalText: "旧回答"}}
	apply := func(n uint64, kind string) {
		raw, _ := json.Marshal(s)
		if e := v.Apply(taskstore.Event{Version: 1, Sequence: n, Kind: kind, Payload: raw}); e != nil {
			t.Fatal(e)
		}
	}
	apply(1, "finished")
	if v.Result == nil {
		t.Fatal("历史终态缺失")
	}
	apply(2, "started")
	apply(3, "model")
	if v.Result != nil || !v.Running {
		t.Fatal("恢复被旧结果污染")
	}
	s.LastResult = &RunResult{Status: BudgetExhausted}
	apply(4, "finished")
	if v.Result.Status != BudgetExhausted {
		t.Fatal("新终态错误")
	}
}
