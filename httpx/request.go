package httpx

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/cloudwego/hertz/pkg/protocol"
	"mime/multipart"
	"net/http"
)

// GetJSON 发送 GET 请求并将响应反序列化到 out。
func (c *Client) GetJSON(ctx context.Context, path string, out any) error {
	return c.DoJSON(ctx, http.MethodGet, path, nil, out)
}

// PostJSON 发送 POST 请求。
func (c *Client) PostJSON(ctx context.Context, path string, body any, out any) error {
	return c.DoJSON(ctx, http.MethodPost, path, body, out)
}

// DoJSON 使用指定的 HTTP 方法发送请求。
func (c *Client) DoJSON(ctx context.Context, method, path string, body any, out any) error {
	priority := GetPriority(ctx)
	if c.priorityQueue != nil {
		if !c.priorityQueue.Acquire(ctx, priority) {
			return fmt.Errorf("%s: request cancelled due to priority queue", c.service)
		}
		defer c.priorityQueue.Release(priority)
	}

	fullURL := c.resolveEndpoint(path)

	req, resp := protocol.AcquireRequest(), protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req)
	defer protocol.ReleaseResponse(resp)

	req.SetMethod(method)
	req.SetRequestURI(fullURL)
	c.applyAuth(req)

	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%s: marshal body: %w", c.service, err)
		}
		req.SetBody(data)
		req.Header.SetContentTypeBytes([]byte("application/json"))
	}

	if err := c.client.Do(ctx, req, resp); err != nil {
		return fmt.Errorf("%s: request failed: %w", c.service, err)
	}
	return c.handleResponse(resp, method+" "+path, out)
}

// DoRaw 使用指定的 HTTP 方法发送请求，返回原始响应字节数据。
func (c *Client) DoRaw(ctx context.Context, method, path string, body any) ([]byte, error) {
	priority := GetPriority(ctx)
	if c.priorityQueue != nil {
		if !c.priorityQueue.Acquire(ctx, priority) {
			return nil, fmt.Errorf("%s: request cancelled due to priority queue", c.service)
		}
		defer c.priorityQueue.Release(priority)
	}

	fullURL := c.resolveEndpoint(path)

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

	if err := c.client.Do(ctx, req, resp); err != nil {
		return nil, fmt.Errorf("%s: request failed: %w", c.service, err)
	}
	if err := c.handleResponse(resp, method+" "+path, nil); err != nil {
		return nil, err
	}
	return append([]byte(nil), resp.Body()...), nil
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

	fullURL := c.resolveEndpoint(path)
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

	if err := c.client.Do(ctx, req, resp); err != nil {
		return fmt.Errorf("%s: upload failed: %w", c.service, err)
	}
	return c.handleResponse(resp, "UPLOAD "+path, out)
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

	fullURL := c.resolveEndpoint(path)
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

	if err := c.client.Do(ctx, req, resp); err != nil {
		return fmt.Errorf("%s: upload failed: %w", c.service, err)
	}
	return c.handleResponse(resp, "UPLOAD "+path, out)
}
func (c *Client) applyAuth(req *protocol.Request) {
	if c.bearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearerToken)
	} else if c.basicUser != "" {
		cred := base64.StdEncoding.EncodeToString([]byte(c.basicUser + ":" + c.basicPass))
		req.Header.Set("Authorization", "Basic "+cred)
	}
}
func (c *Client) handleResponse(resp *protocol.Response, action string, out any) error {
	code := resp.StatusCode()
	if code >= 200 && code < 300 {
		if out != nil {
			if err := json.Unmarshal(resp.Body(), out); err != nil {
				return fmt.Errorf("%s: unmarshal response: %w", c.service, err)
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
