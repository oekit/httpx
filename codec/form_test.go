package codec

import (
	"net/url"
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

// TestFormCodec_IntOverflowErrors 回归（#109）：整数按目标字段位宽解析，
// 越界报错——修复前按 64 位解析后 SetInt 静默截断（300 写进 int8 变 44），
// err 为 nil，与 encoding/json 的越界报错语义及本函数 bool/float 的报错
// 处理自相矛盾。
func TestFormCodec_IntOverflowErrors(t *testing.T) {
	c := GetCodec("form")

	var i8 struct {
		Code int8 `form:"code"`
	}
	err := c.Unmarshal([]byte("code=300"), &i8)
	if err == nil {
		t.Fatalf("300 into int8 must error, got silent truncation to %d", i8.Code)
	}
	if !strings.Contains(err.Error(), "out of range") {
		t.Fatalf("err = %v, want out-of-range", err)
	}
	if i8.Code != 0 {
		t.Fatalf("field written despite error: %d", i8.Code)
	}

	var u8 struct {
		N uint8 `form:"n"`
	}
	if err := c.Unmarshal([]byte("n=256"), &u8); err == nil {
		t.Fatalf("256 into uint8 must error, got %d", u8.N)
	}

	// 位宽内正常值不受影响
	var ok struct {
		Code int8  `form:"code"`
		N    uint8 `form:"n"`
	}
	if err := c.Unmarshal([]byte("code=100&n=200"), &ok); err != nil {
		t.Fatalf("in-range values must decode: %v", err)
	}
	if ok.Code != 100 || ok.N != 200 {
		t.Fatalf("in-range decode mismatch: %+v", ok)
	}
}

// TestFormCodec_CommaSeparatedSliceDecode 回归（#110）：文档承诺「解码同样
// 接受逗号分隔」必须真实成立——外部表单 API 常输出 k=a,b,c，修复前逗号串
// 被整体塞进单个元素；与重复键形态并存（k=1,2&k=3 → [1 2 3]）。
func TestFormCodec_CommaSeparatedSliceDecode(t *testing.T) {
	c := GetCodec("form")

	var out struct {
		Tags []string `form:"tags"`
	}
	if err := c.Unmarshal([]byte("tags=a,b,c"), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Tags) != 3 || out.Tags[0] != "a" || out.Tags[1] != "b" || out.Tags[2] != "c" {
		t.Fatalf("comma-separated decode = %v, want [a b c]", out.Tags)
	}

	// 重复键与逗号形态并存
	var nums struct {
		Nums []int `form:"nums"`
	}
	if err := c.Unmarshal([]byte("nums=1,2&nums=3"), &nums); err != nil {
		t.Fatal(err)
	}
	if len(nums.Nums) != 3 || nums.Nums[0] != 1 || nums.Nums[1] != 2 || nums.Nums[2] != 3 {
		t.Fatalf("mixed decode = %v, want [1 2 3]", nums.Nums)
	}

	// 切分后 TrimSpace
	var sp struct {
		Tags []string `form:"tags"`
	}
	if err := c.Unmarshal([]byte("tags=%20a%20,%20b"), &sp); err != nil {
		t.Fatal(err)
	}
	if len(sp.Tags) != 2 || sp.Tags[0] != "a" || sp.Tags[1] != "b" {
		t.Fatalf("trim decode = %q, want [a b]", sp.Tags)
	}

	// 重复键原形态不回归
	var rk struct {
		Tags []string `form:"tags"`
	}
	if err := c.Unmarshal([]byte("tags=x&tags=y"), &rk); err != nil {
		t.Fatal(err)
	}
	if len(rk.Tags) != 2 || rk.Tags[0] != "x" || rk.Tags[1] != "y" {
		t.Fatalf("repeated-key decode = %v, want [x y]", rk.Tags)
	}

	// 本仓编码的 round-trip 不回归
	enc, err := c.Marshal(&rk)
	if err != nil {
		t.Fatal(err)
	}
	var rt struct {
		Tags []string `form:"tags"`
	}
	if err := c.Unmarshal(enc, &rt); err != nil {
		t.Fatal(err)
	}
	if len(rt.Tags) != 2 || rt.Tags[0] != "x" || rt.Tags[1] != "y" {
		t.Fatalf("round-trip broken: %v", rt.Tags)
	}
}

// TestFormCodec_EmptyTagNameFallsBackToFieldName 回归（#111）：`form:",omitempty"`
// 这类空名 tag 回退字段名——修复前键名变空串：编码出 "=v" 无名参数、解码侧
// 用空键查值恒零值。
func TestFormCodec_EmptyTagNameFallsBackToFieldName(t *testing.T) {
	c := GetCodec("form")
	type payload struct {
		X     string `form:",omitempty"`
		Other string `form:"other"`
	}

	// 编码侧：不得出现空键
	data, err := c.Marshal(&payload{X: "x", Other: "y"})
	if err != nil {
		t.Fatal(err)
	}
	vals, err := url.ParseQuery(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := vals[""]; ok {
		t.Fatalf("empty key present: %q", data)
	}
	if vals.Get("X") != "x" || vals.Get("other") != "y" {
		t.Fatalf("encoded = %q, want X=x&other=y keys", data)
	}

	// 解码侧：对称回退字段名查值
	var out payload
	if err := c.Unmarshal([]byte("X=x&other=y"), &out); err != nil {
		t.Fatal(err)
	}
	if out.X != "x" || out.Other != "y" {
		t.Fatalf("decoded = %+v, want X=x Other=y", out)
	}
}
