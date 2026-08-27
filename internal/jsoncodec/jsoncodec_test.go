package jsoncodec

import (
	"encoding/json"
	"errors"
	"sync"
	"testing"
)

func resetForTest(codec Codec, frozen bool) {
	global.Lock()
	global.codec = codec
	global.frozen = frozen
	global.Unlock()
}

func TestSetAndFreezeLifecycle(t *testing.T) {
	resetForTest(standard{}, false)
	t.Cleanup(func() { resetForTest(standard{}, false) })

	codec := Funcs{MarshalFunc: json.Marshal, UnmarshalFunc: json.Unmarshal}
	if err := Set(codec); err != nil {
		t.Fatal(err)
	}
	if _, err := Marshal(map[string]string{"ok": "yes"}); err != nil {
		t.Fatal(err)
	}
	if err := Set(codec); !errors.Is(err, ErrFrozen) {
		t.Fatalf("set after use error=%v", err)
	}
}

func TestInvalidCodecs(t *testing.T) {
	resetForTest(standard{}, false)
	t.Cleanup(func() { resetForTest(standard{}, false) })

	if err := Set(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("nil codec error=%v", err)
	}
	if err := Set(Funcs{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty funcs error=%v", err)
	}
	var codec *pointerCodec
	if err := Set(codec); !errors.Is(err, ErrInvalid) {
		t.Fatalf("typed nil codec error=%v", err)
	}
}

type pointerCodec struct{}

func (*pointerCodec) Marshal(any) ([]byte, error) { return nil, nil }
func (*pointerCodec) Unmarshal([]byte, any) error { return nil }

func TestConcurrentCurrentFreezesCodec(t *testing.T) {
	resetForTest(standard{}, false)
	t.Cleanup(func() { resetForTest(standard{}, false) })

	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_ = Current()
		}()
	}
	wait.Wait()
	if err := Set(standard{}); !errors.Is(err, ErrFrozen) {
		t.Fatalf("concurrent current did not freeze codec: %v", err)
	}
}
