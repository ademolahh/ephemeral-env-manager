package db

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestCreateAndGetTemplate(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	spec := json.RawMessage(`{"name": "app", "version": "v1", "ttl": "1h"}`)

	if err := CreateTemplate(ctx, tx, "app", "v1", spec); err != nil {
		t.Fatalf("CreateTemplate() error = %v", err)
	}

	got, err := GetTemplate(ctx, tx, "app", "v1")
	if err != nil {
		t.Fatalf("GetTemplate() error = %v", err)
	}

	var m map[string]string
	if err := json.Unmarshal(got, &m); err != nil || m["name"] != "app" || m["ttl"] != "1h" {
		t.Errorf("GetTemplate() = %s, %v, want stored spec", got, err)
	}
}

func TestCreateTemplateDuplicate(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()
	spec := json.RawMessage(`{}`)

	if err := CreateTemplate(ctx, tx, "app", "v1", spec); err != nil {
		t.Fatalf("CreateTemplate() error = %v", err)
	}
	if err := CreateTemplate(ctx, tx, "app", "v1", spec); !errors.Is(err, ErrTemplateExists) {
		t.Errorf("CreateTemplate() duplicate error = %v, want ErrTemplateExists", err)
	}
	if err := CreateTemplate(ctx, tx, "app", "v2", spec); err != nil {
		t.Errorf("CreateTemplate() new version error = %v", err)
	}
}

func TestGetTemplateNotFound(t *testing.T) {
	tx := testTx(t)
	if _, err := GetTemplate(context.Background(), tx, "app", "v1"); !errors.Is(err, ErrTemplateNotFound) {
		t.Errorf("GetTemplate() error = %v, want ErrTemplateNotFound", err)
	}
}

func TestListTemplates(t *testing.T) {
	tx := testTx(t)
	ctx := context.Background()

	got, err := ListTemplates(ctx, tx)
	if err != nil || len(got) != 0 {
		t.Fatalf("ListTemplates() = %s, %v, want empty", got, err)
	}

	for _, tpl := range [][2]string{{"web", "v1"}, {"app", "v2"}, {"app", "v1"}} {
		spec, _ := json.Marshal(map[string]string{"name": tpl[0], "version": tpl[1]})
		if err := CreateTemplate(ctx, tx, tpl[0], tpl[1], spec); err != nil {
			t.Fatalf("CreateTemplate() error = %v", err)
		}
	}

	got, err = ListTemplates(ctx, tx)
	if err != nil {
		t.Fatalf("ListTemplates() error = %v", err)
	}

	want := []string{"app@v1", "app@v2", "web@v1"}
	if len(got) != len(want) {
		t.Fatalf("ListTemplates() = %s, want %d templates", got, len(want))
	}
	for i, spec := range got {
		var m map[string]string
		if err := json.Unmarshal(spec, &m); err != nil || m["name"]+"@"+m["version"] != want[i] {
			t.Errorf("template %d = %s, want %s", i, spec, want[i])
		}
	}
}
