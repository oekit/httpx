package codec

import (
	"encoding/json"
	"testing"
)

func TestJSONCodecBuiltin(t *testing.T) {
	c := GetCodec("json")
	if c == nil {
		t.Fatal("json codec not registered")
	}
	if GetCodec("JSON") == nil {
		t.Fatal("registry should be case-insensitive")
	}
	if c.ContentType() != "application/json" {
		t.Fatalf("content type = %q", c.ContentType())
	}
	data, err := c.Marshal(map[string]int{"ping": 2})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]int
	if err := c.Unmarshal(data, &out); err != nil || out["ping"] != 2 {
		t.Fatalf("roundtrip broken: %v %v", out, err)
	}
}

func TestGetCodecUnknown(t *testing.T) {
	if GetCodec("nope") != nil {
		t.Fatal("unknown codec should be nil")
	}
}

func TestRegisterCodecRoundtrip(t *testing.T) {
	RegisterCodec(&upperCodec{})
	c := GetCodec("UPPER")
	if c == nil {
		t.Fatal("custom codec not registered (case normalization broken)")
	}
	data, err := c.Marshal("hello")
	if err != nil || string(data) != `"HELLO"` {
		t.Fatalf("custom marshal = %s %v", data, err)
	}
	var s string
	if err := c.Unmarshal(data, &s); err != nil || s != "HELLO" {
		t.Fatalf("custom unmarshal = %q %v", s, err)
	}
}

func TestRegisterNilPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("nil codec should panic")
		}
	}()
	RegisterCodec(nil)
}

func TestRegisterEmptyNamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("empty name should panic")
		}
	}()
	RegisterCodec(&emptyNameCodec{})
}

func TestMustGetCodecPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("MustGetCodec unknown should panic")
		}
	}()
	MustGetCodec("nope")
}

type upperCodec struct{}

func (upperCodec) Name() string        { return "UPPER" }
func (upperCodec) ContentType() string { return "application/x-upper" }
func (upperCodec) Marshal(v any) ([]byte, error) {
	s, ok := v.(string)
	if !ok {
		return json.Marshal(v)
	}
	return json.Marshal(upper(s))
}
func (upperCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func upper(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 32
		}
	}
	return string(b)
}

type emptyNameCodec struct{}

func (emptyNameCodec) Name() string                { return "" }
func (emptyNameCodec) ContentType() string         { return "application/x-empty" }
func (emptyNameCodec) Marshal(any) ([]byte, error) { return nil, nil }
func (emptyNameCodec) Unmarshal([]byte, any) error { return nil }
