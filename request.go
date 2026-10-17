package httpx

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/oekit/httpx/codec"
	"github.com/oekit/httpx/loadbalance"
)

// GetJSON 发送 GET 请求并将响应反序列化到 out。
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	return c.DoJSON(ctx, http.MethodGet, path, nil, out)
}

// PostJSON 发送 POST 请求。
func (c *Client) PostJSON(ctx context.Context, path string, body any, out any) error {
	return c.DoJSON(ctx, http.MethodPost, path, body, out)
}

// DoJSON 使用指定的 HTTP 方法发送 JSON 请求（等价 DoCodec(codec.DefaultName, ...)）。
func (c *Client) DoJSON(ctx context.Context, method, path string, body any, out any) error {
	return c.DoCodec(ctx, codec.DefaultName, method, path, body, out)
}

// DoCodec 使用指定编解码器发送请求：body 经 codec.Marshal 序列化并设置
// 对应 Content-Type；响应按其 Content-Type 反查 codec 反序列化到 out
// （查不到回退请求 codec）。
func (c *Client) DoCodec(ctx context.Context, codecName, method, path string, body any, out any) error {
	cd := codec.MustGetCodec(codecName)

	priority := GetPriority(ctx)
	if c.priorityQueue != nil {
		if !c.priorityQueue.Acquire(ctx, priority) {
			return fmt.Errorf("%s: request cancelled due to priority queue", c.service)
		}
		defer c.priorityQueue.Release(priority)
	}

	fullURL, lbDone := c.resolveEndpoint(ctx, path)

	req, resp := protocol.AcquireRequest(), protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req)
	defer protocol.ReleaseResponse(resp)

	req.SetMethod(method)
	req.SetRequestURI(fullURL)
	c.applyAuth(req)

	if body != nil {
		data, err := cd.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s: marshal body: %w", c.service, err)
		}
		req.SetBody(data)
		req.Header.SetContentTypeBytes([]byte(cd.ContentType()))
	}

	err := c.client.Do(ctx, req, resp)
	if lbDone != nil {
		lbDone(ctx, loadbalance.DoneInfo{Err: err})
	}
	if err != nil {
		return fmt.Errorf("%s: request failed: %w", c.service, err)
	}
	return c.handleResponse(resp, method+" "+path, out, cd)
}

// DoRaw 使用指定的 HTTP 方法发送 JSON 请求，返回原始响应字节数据。
func (c *Client) DoRaw(ctx context.Context, method, path string, body any) ([]byte, error) {
	priority := GetPriority(ctx)
	if c.priorityQueue != nil {
		if !c.priorityQueue.Acquire(ctx, priority) {
			return nil, fmt.Errorf("%s: request cancelled due to priority queue", c.service)
		}
		defer c.priorityQueue.Release(priority)
	}

	fullURL, lbDone := c.resolveEndpoint(ctx, path)

	req, resp := protocol.AcquireRequest(), protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req)
	defer protocol.ReleaseResponse(resp)

	req.SetMethod(method)
	req.SetRequestURI(fullURL)
	c.applyAuth(req)

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("%s: marshal body: %w", c.service, err)
		}
		req.SetBody(data)
		req.Header.SetContentTypeBytes([]byte("application/json"))
	}

	err := c.client.Do(ctx, req, resp)
	if lbDone != nil {
		lbDone(ctx, loadbalance.DoneInfo{Err: err})
	}
	if err != nil {
		return nil, fmt.Errorf("%s: request failed: %w", c.service, err)
	}
	if err := c.handleResponse(resp, method+" "+path, nil, nil); err != nil {
		return nil, err
	}
	return append([]byte(nil), resp.Body()...), nil
}

// DoDetail 使用指定的 HTTP 方法发送请求，返回状态码与原始响应体。
// 与 DoJSON/DoRaw 不同：2xx 也返回原始 body（DoJSON 只把 body 解到 out，
// DoRaw 仅在成功时返回 body），非 2xx 同时返回 status、body 与
// *ServiceError，调用方可自行判定如何处理响应内容。
// 传输层失败时 status 为 0、body 为 nil。
func (c *Client) DoDetail(ctx context.Context, method, path string, body any) (int, []byte, error) {
	priority := GetPriority(ctx)
	if c.priorityQueue != nil {
		if !c.priorityQueue.Acquire(ctx, priority) {
			return 0, nil, fmt.Errorf("%s: request cancelled due to priority queue", c.service)
		}
		defer c.priorityQueue.Release(priority)
	}

	fullURL, lbDone := c.resolveEndpoint(ctx, path)

	req, resp := protocol.AcquireRequest(), protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req)
	defer protocol.ReleaseResponse(resp)

	req.SetMethod(method)
	req.SetRequestURI(fullURL)
	c.applyAuth(req)

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("%s: marshal body: %w", c.service, err)
		}
		req.SetBody(data)
		req.Header.SetContentTypeBytes([]byte("application/json"))
	}

	err := c.client.Do(ctx, req, resp)
	if lbDone != nil {
		lbDone(ctx, loadbalance.DoneInfo{Err: err})
	}
	if err != nil {
		return 0, nil, fmt.Errorf("%s: request failed: %w", c.service, err)
	}
	status := resp.StatusCode()
	raw := append([]byte(nil), resp.Body()...)
	if err := c.handleResponse(resp, method+" "+path, nil, nil); err != nil {
		return status, raw, err
	}
	return status, raw, nil
}

// UploadFile 上传文件 (multipart/form-data)。
func (c *Client) UploadFile(ctx context.Context, path, filePath, fieldName string, extraFields map[string]string, out any) error {
	priority := GetPriority(ctx)
	if c.priorityQueue != nil {
		if !c.priorityQueue.Acquire(ctx, priority) {
			return fmt.Errorf("%s: request cancelled due to priority queue", c.service)
		}
		defer c.priorityQueue.Release(priority)
	}

	fullURL, lbDone := c.resolveEndpoint(ctx, path)
	body, contentType, err := buildMultipartFileBytes(fieldName, filePath, nil, extraFields)
	if err != nil {
		return fmt.Errorf("%s: build multipart: %w", c.service, err)
	}

	req, resp := protocol.AcquireRequest(), protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req)
	defer protocol.ReleaseResponse(resp)

	req.SetMethod(http.MethodPost)
	req.SetRequestURI(fullURL)
	c.applyAuth(req)
	req.SetBody(body)
	req.Header.SetContentTypeBytes([]byte(contentType))

	err = c.client.Do(ctx, req, resp)
	if lbDone != nil {
		lbDone(ctx, loadbalance.DoneInfo{Err: err})
	}
	if err != nil {
		return fmt.Errorf("%s: upload failed: %w", c.service, err)
	}
	return c.handleResponse(resp, "UPLOAD "+path, out, nil)
}

// UploadBytes 上传字节数据 (multipart/form-data)。
func (c *Client) UploadBytes(ctx context.Context, path string, data []byte, fieldName, fileName string, extraFields map[string]string, out any) error {
	priority := GetPriority(ctx)
	if c.priorityQueue != nil {
		if !c.priorityQueue.Acquire(ctx, priority) {
			return fmt.Errorf("%s: request cancelled due to priority queue", c.service)
		}
		defer c.priorityQueue.Release(priority)
	}

	fullURL, lbDone := c.resolveEndpoint(ctx, path)
	body, contentType, err := buildMultipartFileBytes(fieldName, fileName, data, extraFields)
	if err != nil {
		return fmt.Errorf("%s: build multipart: %w", c.service, err)
	}

	req, resp := protocol.AcquireRequest(), protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req)
	defer protocol.ReleaseResponse(resp)

	req.SetMethod(http.MethodPost)
	req.SetRequestURI(fullURL)
	c.applyAuth(req)
	req.SetBody(body)
	req.Header.SetContentTypeBytes([]byte(contentType))

	err = c.client.Do(ctx, req, resp)
	if lbDone != nil {
		lbDone(ctx, loadbalance.DoneInfo{Err: err})
	}
	if err != nil {
		return fmt.Errorf("%s: upload failed: %w", c.service, err)
	}
	return c.handleResponse(resp, "UPLOAD "+path, out, nil)
}

func (c *Client) applyAuth(req *protocol.Request) {
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	} else if c.basicUser != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(c.basicUser + ":" + c.basicPass))
		req.Header.Set("Authorization", "Basic "+cred)
	}
}

// handleResponse 校验响应状态码并反序列化输出。
// cd 非 nil 时按其 Content-Type 优先协商（响应头反查，查不到回退 cd）；
// cd 为 nil 时按 encoding/json 直接解析（历史行为，Upload/DoRaw 路径）。
func (c *Client) handleResponse(resp *protocol.Response, action string, out any, cd codec.Codec) error {
	code := resp.StatusCode()
	if code >= 200 && code < 300 {
		if out != nil {
			var decodeErr error
			switch {
			case cd != nil:
				decoder := responseCodec(resp, cd)
				decodeErr = decoder.Unmarshal(resp.Body(), out)
			default:
				decodeErr = json.Unmarshal(resp.Body(), out)
			}
			if decodeErr != nil {
				return fmt.Errorf("%s: unmarshal response: %w", c.service, decodeErr)
			}
		}
		return nil
	}
	msg := string(resp.Body())
	if len(msg) > 500 {
		msg = msg[:500]
	}
	return &ServiceError{
		Service: c.service,
		Action:  action,
		Code:    code,
		Message: msg,
	}
}

// responseCodec 按响应 Content-Type 反查 codec（截断 "; charset" 后缀），
// 未注册时回退请求 codec。
func responseCodec(resp *protocol.Response, fallback codec.Codec) codec.Codec {
	ct := resp.Header.Get("Content-Type")
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = ct[:i]
	}
	ct = strings.TrimSpace(strings.ToLower(strings.TrimPrefix(ct, "application/")))
	if ct == "" {
		return fallback
	}
	if c := codec.GetCodec(ct); c != nil {
		return c
	}
	// 常见全名兜底（codec 注册名可能为完整 content-type）
	if c := codec.GetCodec("application/" + ct); c != nil {
		return c
	}
	return fallback
}

func buildMultipartFileBytes(fieldName, fileName string, data []byte, extraFields map[string]string) ([]byte, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	part, err := w.CreateFormFile(fieldName, fileName)
	if err != nil {
		return nil, "", err
	}
	if data != nil {
		if _, err := part.Write(data); err != nil {
			return nil, "", err
		}
	}

	for k, v := range extraFields {
		if err := w.WriteField(k, v); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return buf.Bytes(), w.FormDataContentType(), nil
}
