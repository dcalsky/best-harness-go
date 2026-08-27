// Package jsoncodec owns the process-wide JSON implementation used by the SDK.
package jsoncodec

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
)

// RawMessage is the raw-JSON type used throughout the SDK.
type RawMessage = json.RawMessage

// Codec is the JSON serialization boundary used throughout the SDK.
// Implementations must be safe for concurrent use and must honor the standard
// json tags and MarshalJSON/UnmarshalJSON contracts.
type Codec interface {
	Marshal(any) ([]byte, error)
	Unmarshal([]byte, any) error
}

// Funcs adapts package-level marshal and unmarshal functions to Codec.
type Funcs struct {
	MarshalFunc   func(any) ([]byte, error)
	UnmarshalFunc func([]byte, any) error
}

func (f Funcs) Marshal(value any) ([]byte, error) {
	if f.MarshalFunc == nil {
		return nil, fmt.Errorf("%w: marshal function is required", ErrInvalid)
	}
	return f.MarshalFunc(value)
}

func (f Funcs) Unmarshal(data []byte, target any) error {
	if f.UnmarshalFunc == nil {
		return fmt.Errorf("%w: unmarshal function is required", ErrInvalid)
	}
	return f.UnmarshalFunc(data, target)
}

var (
	ErrInvalid = errors.New("invalid JSON codec")
	ErrFrozen  = errors.New("JSON codec is frozen")
)

type standard struct{}

func (standard) Marshal(value any) ([]byte, error) { return json.Marshal(value) }
func (standard) Unmarshal(data []byte, target any) error {
	return json.Unmarshal(data, target)
}

// Standard returns the SDK's encoding/json implementation.
func Standard() Codec { return standard{} }

var global = struct {
	sync.Mutex
	codec  Codec
	frozen bool
}{codec: standard{}}

// Set replaces the process-wide codec before its first use.
func Set(codec Codec) error {
	global.Lock()
	defer global.Unlock()
	if global.frozen {
		return ErrFrozen
	}
	if err := validate(codec); err != nil {
		return err
	}
	global.codec = codec
	return nil
}

// Current returns and freezes the configured process-wide codec.
func Current() Codec {
	global.Lock()
	global.frozen = true
	codec := global.codec
	global.Unlock()
	return codec
}

func Marshal(value any) ([]byte, error)       { return Current().Marshal(value) }
func Unmarshal(data []byte, target any) error { return Current().Unmarshal(data, target) }

type taggedProbe struct {
	Name string          `json:"name"`
	Raw  json.RawMessage `json:"raw"`
	Zero string          `json:"zero,omitempty"`
}

type marshalProbe struct{}

func (marshalProbe) MarshalJSON() ([]byte, error) {
	return []byte(`{"custom":true}`), nil
}

type unmarshalProbe struct{ Called bool }

func (p *unmarshalProbe) UnmarshalJSON(data []byte) error {
	if !bytes.Equal(bytes.TrimSpace(data), []byte(`{"custom":true}`)) {
		return errors.New("unexpected custom JSON payload")
	}
	p.Called = true
	return nil
}

func validate(codec Codec) error {
	if isNil(codec) {
		return fmt.Errorf("%w: codec is required", ErrInvalid)
	}
	if funcs, ok := codec.(Funcs); ok {
		if funcs.MarshalFunc == nil || funcs.UnmarshalFunc == nil {
			return fmt.Errorf("%w: marshal and unmarshal functions are required", ErrInvalid)
		}
	}

	encoded, err := codec.Marshal(taggedProbe{Name: "probe", Raw: json.RawMessage(`{"ok":true}`)})
	if err != nil {
		return fmt.Errorf("%w: marshal probe: %v", ErrInvalid, err)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		return fmt.Errorf("%w: codec returned invalid JSON: %v", ErrInvalid, err)
	}
	if string(object["name"]) != `"probe"` || string(object["raw"]) != `{"ok":true}` {
		return fmt.Errorf("%w: codec does not preserve json tags or raw JSON", ErrInvalid)
	}
	if _, exists := object["zero"]; exists {
		return fmt.Errorf("%w: codec does not honor the omitempty tag", ErrInvalid)
	}

	var decoded taggedProbe
	if err := codec.Unmarshal([]byte(`{"name":"probe","raw":{"ok":true}}`), &decoded); err != nil {
		return fmt.Errorf("%w: unmarshal probe: %v", ErrInvalid, err)
	}
	if decoded.Name != "probe" || string(decoded.Raw) != `{"ok":true}` {
		return fmt.Errorf("%w: codec does not preserve json tags or raw JSON", ErrInvalid)
	}

	encoded, err = codec.Marshal(marshalProbe{})
	if err != nil || !bytes.Equal(bytes.TrimSpace(encoded), []byte(`{"custom":true}`)) {
		return fmt.Errorf("%w: codec does not honor MarshalJSON", ErrInvalid)
	}
	var custom unmarshalProbe
	if err := codec.Unmarshal([]byte(`{"custom":true}`), &custom); err != nil || !custom.Called {
		return fmt.Errorf("%w: codec does not honor UnmarshalJSON", ErrInvalid)
	}
	return nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	v := reflect.ValueOf(value)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}
