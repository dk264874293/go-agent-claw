package taskstore

import (
	"bytes"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestArtifactPagesReconstructUTF8Source(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	task := createTestTask(t, store, NewID())
	for _, source := range []string{"", "中文🙂无末尾换行", strings.Repeat("中文🙂abc\n", 1200)} {
		id := NewID()
		if err := task.SaveArtifact(id, []byte(source)); err != nil {
			t.Fatal(err)
		}
		for _, limit := range []int{4, 7, 4000} {
			var got bytes.Buffer
			offset, pages := 0, 0
			for {
				page, err := task.ReadArtifactPage(id, offset, limit)
				if err != nil {
					t.Fatal(err)
				}
				if !utf8.Valid(page.Data) || len(page.Data) > limit || page.Total != int64(len(source)) ||
					page.Offset != int64(offset) || page.NextOffset != int64(offset+len(page.Data)) {
					t.Fatalf("材料分页破坏了范围或字符边界: %+v", page)
				}
				got.Write(page.Data)
				pages++
				if page.EOF {
					if page.NextOffset != int64(len(source)) {
						t.Fatalf("提前到达结尾: %+v", page)
					}
					break
				}
				if page.NextOffset <= int64(offset) || pages > len(source)+1 {
					t.Fatalf("分页没有前进: %+v", page)
				}
				offset = int(page.NextOffset)
			}
			if got.String() != source {
				t.Fatalf("分页未重建源材料: limit=%d", limit)
			}
		}
	}
}

func TestArtifactPageRejectsSplitCharacterAndPreservesRawRead(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	task := createTestTask(t, store, NewID())
	id := NewID()
	if err := task.SaveArtifact(id, []byte("中文🙂尾")); err != nil {
		t.Fatal(err)
	}
	for _, offset := range []int{1, 2, 4, 5, 7, 8, 9} {
		_, err := task.ReadArtifactPage(id, offset, 4)
		if err == nil || !strings.Contains(err.Error(), "字符中间") || !strings.Contains(err.Error(), "请使用 offset") {
			t.Fatalf("字符中 offset %d 未获得纠正提示: %v", offset, err)
		}
	}
	if _, err := task.ReadArtifactPage(id, 0, 1); err == nil || !strings.Contains(err.Error(), "至少 3 字节") {
		t.Fatalf("不能容纳字符的 limit 未被拒绝: %v", err)
	}
	got, err := task.ReadArtifact(id, 1, 1)
	if err != nil || !bytes.Equal(got, []byte("中")[1:2]) {
		t.Fatalf("原始字节接口行为改变: %x, %v", got, err)
	}
	for _, offset := range []int{len("中文🙂尾"), 100} {
		page, err := task.ReadArtifactPage(id, offset, 4)
		if err != nil || len(page.Data) != 0 || !page.EOF || page.Total != int64(len("中文🙂尾")) {
			t.Fatalf("尾后空页行为改变: %+v, %v", page, err)
		}
	}
}

func TestArtifactPageRemainsTaskScoped(t *testing.T) {
	store := openTestStore(t, t.TempDir())
	first := createTestTask(t, store, NewID())
	second := createTestTask(t, store, NewID())
	id := NewID()
	if err := first.SaveArtifact(id, []byte("当前任务")); err != nil {
		t.Fatal(err)
	}
	if _, err := second.ReadArtifactPage(id, 0, 4); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("跨任务材料读取未拒绝: %v", err)
	}
	for _, input := range []struct {
		id            string
		offset, limit int
	}{{"../" + id, 0, 4}, {id, -1, 4}, {id, 0, 0}} {
		if _, err := first.ReadArtifactPage(input.id, input.offset, input.limit); err == nil {
			t.Fatalf("无效参数未拒绝: %+v", input)
		}
	}
}
