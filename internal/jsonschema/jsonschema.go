// Package jsonschema provides a lightweight, deterministic representation of
// the JSON Schema subset commonly used by model tool definitions.
package jsonschema

import (
	"bytes"
	"fmt"
	json "github.com/dcalsky/best-harness-go/internal/jsoncodec"
	"sort"
)

// DataType is a JSON Schema type name.
type DataType string

const (
	Object  DataType = "object"
	Number  DataType = "number"
	Integer DataType = "integer"
	String  DataType = "string"
	Array   DataType = "array"
	Null    DataType = "null"
	Boolean DataType = "boolean"
)

// Definition describes the JSON Schema subset used for tool arguments.
// PropertyOrder affects JSON rendering only and is not emitted as a keyword.
type Definition struct {
	Type                 DataType              `json:"type,omitempty"`
	Description          string                `json:"description,omitempty"`
	Enum                 []string              `json:"enum,omitempty"`
	Properties           map[string]Definition `json:"properties,omitempty"`
	PropertyOrder        []string              `json:"-"`
	Required             []string              `json:"required,omitempty"`
	Items                *Definition           `json:"items,omitempty"`
	AdditionalProperties any                   `json:"additionalProperties,omitempty"`
}

// IsZero reports whether d has no explicitly configured schema fields.
func (d Definition) IsZero() bool {
	return d.Type == "" && d.Description == "" && len(d.Enum) == 0 && d.Properties == nil &&
		len(d.PropertyOrder) == 0 && len(d.Required) == 0 && d.Items == nil && d.AdditionalProperties == nil
}

// MarshalJSON renders properties deterministically. Keys listed in
// PropertyOrder are emitted first; remaining keys are sorted by name.
func (d Definition) MarshalJSON() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	wrote := false
	write := func(name string, value any) error {
		raw, err := json.Marshal(value)
		if err != nil {
			return err
		}
		return writeRawField(&buf, &wrote, name, raw)
	}

	if d.Type != "" {
		if err := write("type", d.Type); err != nil {
			return nil, err
		}
	}
	if d.Description != "" {
		if err := write("description", d.Description); err != nil {
			return nil, err
		}
	}
	if len(d.Enum) > 0 {
		if err := write("enum", d.Enum); err != nil {
			return nil, err
		}
	}
	if d.Properties != nil {
		raw, err := d.marshalProperties()
		if err != nil {
			return nil, err
		}
		if err := writeRawField(&buf, &wrote, "properties", raw); err != nil {
			return nil, err
		}
	}
	if len(d.Required) > 0 {
		if err := write("required", d.Required); err != nil {
			return nil, err
		}
	}
	if d.Items != nil {
		if err := write("items", d.Items); err != nil {
			return nil, err
		}
	}
	if d.AdditionalProperties != nil {
		switch v := d.AdditionalProperties.(type) {
		case bool, Definition:
			if err := write("additionalProperties", v); err != nil {
				return nil, err
			}
		case *Definition:
			if v == nil {
				return nil, invalidAdditionalPropertiesError(d.AdditionalProperties)
			}
			if err := write("additionalProperties", v); err != nil {
				return nil, err
			}
		default:
			return nil, invalidAdditionalPropertiesError(d.AdditionalProperties)
		}
	}

	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func invalidAdditionalPropertiesError(value any) error {
	return fmt.Errorf("jsonschema: additionalProperties must be bool, Definition, or *Definition, got %T", value)
}

func (d Definition) marshalProperties() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteByte('{')
	wrote := false
	seen := make(map[string]bool, len(d.PropertyOrder))

	for _, name := range d.PropertyOrder {
		if seen[name] {
			continue
		}
		definition, ok := d.Properties[name]
		if !ok {
			continue
		}
		raw, err := json.Marshal(definition)
		if err != nil {
			return nil, err
		}
		if err := writeRawField(&buf, &wrote, name, raw); err != nil {
			return nil, err
		}
		seen[name] = true
	}

	names := make([]string, 0, len(d.Properties))
	for name := range d.Properties {
		if !seen[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		raw, err := json.Marshal(d.Properties[name])
		if err != nil {
			return nil, err
		}
		if err := writeRawField(&buf, &wrote, name, raw); err != nil {
			return nil, err
		}
	}

	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func writeRawField(buf *bytes.Buffer, wrote *bool, name string, raw []byte) error {
	encodedName, err := json.Marshal(name)
	if err != nil {
		return err
	}
	if *wrote {
		buf.WriteByte(',')
	} else {
		*wrote = true
	}
	buf.Write(encodedName)
	buf.WriteByte(':')
	buf.Write(raw)
	return nil
}
