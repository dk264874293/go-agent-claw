package tools

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestDefinitionRequired(t *testing.T) {
	for _, tc := range []struct {
		name     string
		required []string
	}{
		{name: "omitted"},
		{name: "empty", required: []string{}},
		{name: "single", required: []string{"path"}},
		{name: "ordered", required: []string{"version", "path", "content"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			props := map[string]any{
				"path":    stringParam("path description"),
				"version": stringParam("version description"),
				"content": stringParam("content description"),
			}
			def := definition("test", "description", props, tc.required...)
			if def.Name != "test" || def.Description != "description" {
				t.Fatalf("tool metadata changed: %+v", def)
			}
			want := map[string]any{
				"type": "object", "properties": props, "additionalProperties": false,
			}
			if len(tc.required) > 0 {
				want["required"] = tc.required
			}
			if !reflect.DeepEqual(def.InputSchema, want) {
				t.Fatalf("schema = %#v, want %#v", def.InputSchema, want)
			}

			data, err := json.Marshal(def.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(data, &fields); err != nil {
				t.Fatal(err)
			}
			raw, exists := fields["required"]
			if len(tc.required) == 0 {
				if exists {
					t.Fatalf("required must be omitted: %s", data)
				}
			} else {
				wantJSON, err := json.Marshal(tc.required)
				if err != nil {
					t.Fatal(err)
				}
				if string(raw) != string(wantJSON) {
					t.Fatalf("required = %s, want %s", raw, wantJSON)
				}
			}
		})
	}
}

func TestListDirDefinitionOmitsRequired(t *testing.T) {
	data, err := json.Marshal(NewListDirTool(".").Definition().InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, exists := fields["required"]; exists {
		t.Fatalf("list_dir must omit required: %s", data)
	}
}
