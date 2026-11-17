package httpx

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/protocol"
)

// TestHandleResponse_Non2xxBodyRuneSafeTruncation 回归（#253）：非 2xx 错误体
// 摘要此前按字节截断 msg[:500]，中文/emoji 等多字节 UTF-8 错误体恰好在第
// 500 字节处被腰斩——半个字符落进 ServiceError.Message 成为非法 UTF-8，
// 下游 JSON 序列化/日志采集再编码即乱码。修后走 stringx.FirstN 按 rune 边界
// 截断：结果恒为合法 UTF-8、rune 数 ≤500、且是原错误体的前缀；不拼省略号
// （错误摘要要的是原文前缀，不是装饰）。
func TestHandleResponse_Non2xxBodyRuneSafeTruncation(t *testing.T) {
	c := &Client{service: "utf8-trunc"}
	mkResp := func(body string) *protocol.Response {
		resp := protocol.AcquireResponse()
		t.Cleanup(func() { protocol.ReleaseResponse(resp) })
		resp.SetStatusCode(http.StatusInternalServerError)
		resp.SetBody([]byte(body))
		return resp
	}

	// 各体均构造为「字节 500 落在多字节字符中间」的形态，修复前必产非法 UTF-8：
	// 中文三字节/符——第 500 字节是第 167 个「中」的第 2 字节；
	// emoji 四字节/符——第 500 字节是第 125 个 😀 的第 4 字节；
	// 混排——中文段（字节 1-900）与 emoji 段（字节 901 起）边界均覆盖。
	zh600 := strings.Repeat("中", 600)                             // 600 rune / 1800 字节
	emoji600 := strings.Repeat("😀", 600)                          // 600 rune / 2400 字节
	mixed := strings.Repeat("错码", 150) + strings.Repeat("🔀", 250) // 550 rune / 1900 字节

	cases := []struct {
		name      string
		body      string
		wantRunes int // 截断后期望 rune 数；-1 = 不截断（rune 数 ≤500，原样保留）
	}{
		{"中文错误体字节 500 腰斩面", zh600, 500},
		{"emoji 错误体字节 500 腰斩面", emoji600, 500},
		{"中文+emoji 混排腰斩面", mixed, 500},
		{"纯 ASCII 超长按 500 rune 截断", strings.Repeat("x", 501), 500},
		{"501 字节但仅 167 rune 的中文体按 rune 口径不截断", strings.Repeat("中", 167), -1},
		{"短错误体原样保留", "boom: 订单不存在", -1},
		{"空错误体", "", -1},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := c.handleResponse(mkResp(tc.body), "GET /x", nil, nil)
			if err == nil {
				t.Fatal("err = nil, want *ServiceError")
			}
			svcErr, ok := err.(*ServiceError)
			if !ok {
				t.Fatalf("err = %T, want *ServiceError", err)
			}
			msg := svcErr.Message
			if !utf8.ValidString(msg) {
				t.Fatalf("Message 含非法 UTF-8（rune 腰斩未修）：%q", msg)
			}
			if n := utf8.RuneCountInString(msg); n > 500 {
				t.Fatalf("Message rune 数 = %d, want ≤ 500", n)
			}
			if !strings.HasPrefix(tc.body, msg) {
				t.Fatalf("Message 不是原错误体前缀：msg=%q body 长度=%d", msg, len(tc.body))
			}
			if strings.Contains(msg, "…") || strings.Contains(msg, "...") {
				t.Fatalf("错误摘要不应拼省略号：%q", msg)
			}
			if tc.wantRunes >= 0 {
				if n := utf8.RuneCountInString(msg); n != tc.wantRunes {
					t.Fatalf("Message rune 数 = %d, want %d", n, tc.wantRunes)
				}
			} else if msg != tc.body {
				t.Fatalf("rune 数 ≤500 应原样保留：got %q, want %q", msg, tc.body)
			}
		})
	}
}
