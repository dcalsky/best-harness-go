package jsonschema_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dcalsky/best-harness-go/internal/jsonschema"
)

func TestDefinitionMarshalPreservesPropertyOrder(t *testing.T) {
	definition := jsonschema.Definition{
		Type:          jsonschema.Object,
		PropertyOrder: []string{"task", "dataset_id", "table_config", "task", "missing"},
		Properties: map[string]jsonschema.Definition{
			"z_extra": {Type: jsonschema.Boolean},
			"dataset_id": {
				Type: jsonschema.String,
			},
			"task": {Type: jsonschema.String},
			"table_config": {
				Type:          jsonschema.Object,
				PropertyOrder: []string{"rows", "columns", "values"},
				Properties: map[string]jsonschema.Definition{
					"values":  {Type: jsonschema.Array, Items: &jsonschema.Definition{Type: jsonschema.String}},
					"columns": {Type: jsonschema.Array, Items: &jsonschema.Definition{Type: jsonschema.String}},
					"rows":    {Type: jsonschema.Array, Items: &jsonschema.Definition{Type: jsonschema.String}},
				},
				AdditionalProperties: false,
			},
		},
		Required:             []string{"dataset_id", "task"},
		AdditionalProperties: false,
	}

	raw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	assertOrdered(t, text, `"task":`, `"dataset_id":`, `"table_config":`, `"z_extra":`)
	assertOrdered(t, text, `"rows":`, `"columns":`, `"values":`)
	if !strings.Contains(text, `"required":["dataset_id","task"]`) || !strings.Contains(text, `"additionalProperties":false`) {
		t.Fatalf("schema=%s", text)
	}
}

func TestDefinitionMarshalAdditionalPropertiesSchema(t *testing.T) {
	raw, err := json.Marshal(jsonschema.Definition{
		Type:                 jsonschema.Object,
		AdditionalProperties: jsonschema.Definition{Type: jsonschema.Integer},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(raw); got != `{"type":"object","additionalProperties":{"type":"integer"}}` {
		t.Fatalf("schema=%s", got)
	}
}

func TestDefinitionRejectsInvalidAdditionalProperties(t *testing.T) {
	_, err := json.Marshal(jsonschema.Definition{Type: jsonschema.Object, AdditionalProperties: "false"})
	if err == nil || !strings.Contains(err.Error(), "additionalProperties") {
		t.Fatalf("error=%v", err)
	}
	var nested *jsonschema.Definition
	_, err = json.Marshal(jsonschema.Definition{Type: jsonschema.Object, AdditionalProperties: nested})
	if err == nil || !strings.Contains(err.Error(), "additionalProperties") {
		t.Fatalf("nil pointer error=%v", err)
	}
}

func assertOrdered(t *testing.T, text string, parts ...string) {
	t.Helper()
	previous := -1
	for _, part := range parts {
		at := strings.Index(text, part)
		if at < 0 || at <= previous {
			t.Fatalf("%q is not in order in %s", part, text)
		}
		previous = at
	}
}
