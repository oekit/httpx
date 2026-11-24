package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/oekit/httpx/codec"
)

// TestEmptyBodySuccessSkipsUnmarshal 回归（#237）：2xx 空响应体 + out 非 nil
// 必须按成功返回、out 保持零值——修复前 handleResponse 对 2xx 只要 out 非 nil
// 就无条件反序列化，204 No Content / 200 空 body 被
// json.Unmarshal([]byte{}, out) 判成 "unexpected end of JSON input"，
// 一类正常成功被系统性误报成失败。
//
// 两个反序列化分支都钉：cd 非 nil（DoJSON/DoCodec 走 Content-Type 协商）与
// cd 为 nil（Upload* 走 encoding/json 直解）。
func TestEmptyBodySuccessSkipsUnmarshal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/204":
			w.WriteHeader(http.StatusNoContent)
		case "/empty200":
			w.WriteHeader(http.StatusOK) // 无 body 写入 ⇒ Content-Length: 0
		case "/badjson":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("not-json"))
		case "/goodjson":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"ok":true}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	t.Cleanup(srv.Close)

	t.Run("204 DoJSON 保持零值", func(t *testing.T) {
		c := MustNewClient("empty-204", WithBaseURL(srv.URL))
		t.Cleanup(c.Close)
		var out map[string]any
		if err := c.GetJSON(context.Background(), "/204", &out); err != nil {
			t.Fatalf("204 + out 非 nil: %v, want nil（空体成功不得判 unmarshal 错）", err)
		}
		if out != nil {
			t.Fatalf("out = %v, want 保持零值 nil", out)
		}
	})

	t.Run("200 空 body DoJSON 保持零值", func(t *testing.T) {
		c := MustNewClient("empty-200", WithBaseURL(srv.URL))
		t.Cleanup(c.Close)
		var out map[string]any
		if err := c.GetJSON(context.Background(), "/empty200", &out); err != nil {
			t.Fatalf("200 空 body + out 非 nil: %v, want nil", err)
		}
		if out != nil {
			t.Fatalf("out = %v, want 保持零值 nil", out)
		}
	})

	t.Run("204 UploadBytes 走 cd=nil 分支", func(t *testing.T) {
		c := MustNewClient("empty-upload", WithBaseURL(srv.URL))
		t.Cleanup(c.Close)
		var out map[string]any
		if err := c.UploadBytes(context.Background(), "/204", nil, "file", "f.txt", nil, &out); err != nil {
			t.Fatalf("204 空体上传 + out 非 nil: %v, want nil", err)
		}
		if out != nil {
			t.Fatalf("out = %v, want 保持零值 nil", out)
		}
	})

	t.Run("204 DoDetail 返回状态与空体", func(t *testing.T) {
		c := MustNewClient("empty-detail", WithBaseURL(srv.URL))
		t.Cleanup(c.Close)
		status, body, err := c.DoDetail(context.Background(), http.MethodDelete, "/204", nil)
		if err != nil {
			t.Fatalf("DoDetail 204: %v, want nil", err)
		}
		if status != http.StatusNoContent {
			t.Fatalf("status = %d, want 204", status)
		}
		if len(body) != 0 {
			t.Fatalf("body = %q, want empty", body)
		}
	})

	t.Run("非空非法 body 仍判 unmarshal 错", func(t *testing.T) {
		// 钉住修复面只到「空体」：非空但不是合法 JSON 依旧是解码错误
		c := MustNewClient("bad-json", WithBaseURL(srv.URL))
		t.Cleanup(c.Close)
		var out map[string]any
		err := c.GetJSON(context.Background(), "/badjson", &out)
		if err == nil || !strings.Contains(err.Error(), "unmarshal response") {
			t.Fatalf("err = %v, want unmarshal response 错", err)
		}
	})

	t.Run("合法 body 照常反序列化", func(t *testing.T) {
		c := MustNewClient("good-json", WithBaseURL(srv.URL))
		t.Cleanup(c.Close)
		var out map[string]any
		if err := c.GetJSON(context.Background(), "/goodjson", &out); err != nil {
			t.Fatalf("GetJSON: %v", err)
		}
		if out["ok"] != true {
			t.Fatalf("out = %v, want ok=true", out)
		}
	})
}

// TestHandleResponse_EmptyBodyDirect 单元面钉 handleResponse 本身：空体成功
// 返回 nil 且不动 out；非空体照常解码（与端到端用例互补，省掉传输层噪声）。
func TestHandleResponse_EmptyBodyDirect(t *testing.T) {
	c := &Client{service: "unit"}
	cd := codec.GetCodec(codec.DefaultName)
	mkResp := func(code int, body string) *protocol.Response {
		resp := protocol.AcquireResponse()
		t.Cleanup(func() { protocol.ReleaseResponse(resp) })
		resp.SetStatusCode(code)
		resp.SetBody([]byte(body))
		return resp
	}

	var out map[string]any
	if err := c.handleResponse(mkResp(http.StatusNoContent, ""), "GET /x", &out, cd); err != nil {
		t.Fatalf("204 空体: %v, want nil", err)
	}
	if out != nil {
		t.Fatalf("out = %v, want nil", out)
	}

	if err := c.handleResponse(mkResp(http.StatusOK, `{"n":1}`), "GET /x", &out, cd); err != nil {
		t.Fatalf("200 合法体: %v", err)
	}
	if out == nil || out["n"] != float64(1) {
		t.Fatalf("out = %v, want n=1", out)
	}

	var out2 map[string]any
	err := c.handleResponse(mkResp(http.StatusOK, "{"), "GET /x", &out2, cd)
	if err == nil || !strings.Contains(err.Error(), "unmarshal response") {
		t.Fatalf("err = %v, want unmarshal response 错", err)
	}
}
