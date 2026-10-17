package codec

import (
	"strings"
	"testing"
)

type formPayload struct {
	Name   string   `form:"name"`
	Age    int      `form:"age"`
	Active bool     `form:"active"`
	Score  float64  `form:"score"`
	Tags   []string `form:"tags"`
	_      string   // 非导出语义由 SkipDash 用例覆盖
}

func TestFormCodec_RoundTrip(t *testing.T) {
	RegisterCodec(FormCodec{}) // 幂等（init 已注册）
	c := GetCodec("form")

	in := formPayload{Name: "alice", Age: 30, Active: true, Score: 1.5, Tags: []string{"a", "b"}}
	data, err := c.Marshal(&in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "name=alice") || !strings.Contains(string(data), "tags=a") {
		t.Fatalf("encoded = %q", data)
	}

	var out formPayload
	if err := c.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Name != in.Name || out.Age != in.Age || out.Active != in.Active || out.Score != in.Score {
		t.Fatalf("scalar roundtrip mismatch: %+v vs %+v", out, in)
	}
	if len(out.Tags) != 2 || out.Tags[0] != "a" || out.Tags[1] != "b" {
		t.Fatalf("slice roundtrip mismatch: %v", out.Tags)
	}
}

func TestFormCodec_OmitAndDefaults(t *testing.T) {
	c := GetCodec("form")
	data, err := c.Marshal(&formPayload{Name: "bob"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "internal") {
		t.Fatal("unexported field should be skipped")
	}
	var out formPayload
	if err := c.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	if out.Age != 0 {
		t.Fatalf("missing key should keep zero value, got %d", out.Age)
	}
}

func TestFormCodec_SkipDash(t *testing.T) {
	type skippy struct {
		Visible string `form:"visible"`
		Hidden  string `form:"-"`
	}
	data, err := GetCodec("form").Marshal(&skippy{Visible: "v", Hidden: "h"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "Hidden") || strings.Contains(string(data), "h=") {
		t.Fatalf("`form:\"-\"` should be skipped: %q", data)
	}
}

func TestFormContentType(t *testing.T) {
	if got := GetCodec("form").ContentType(); got != "application/x-www-form-urlencoded" {
		t.Fatalf("content type = %q", got)
	}
}
