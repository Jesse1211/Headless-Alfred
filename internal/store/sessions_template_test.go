package store

import (
	"encoding/json"
	"testing"
)

func TestSessionMeta_TemplateID_Roundtrip(t *testing.T) {
	in := SessionMeta{ID: "X", Name: "n", TemplateID: "summary-todo"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out SessionMeta
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if out.TemplateID != "summary-todo" {
		t.Errorf("TemplateID=%q after roundtrip, want summary-todo", out.TemplateID)
	}
}

func TestSessionMeta_TemplateID_OmittedWhenEmpty(t *testing.T) {
	in := SessionMeta{ID: "X", Name: "n"}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); contains(got, `"template_id"`) {
		t.Errorf("expected template_id to be omitted when empty; got %s", got)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
