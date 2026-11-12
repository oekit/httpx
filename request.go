package httpx

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
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

// guardPriority 获取分级准入许可（请求骨架第一环，#159 收敛单源）。
// 队列满且 ctx 取消/超时时返回错误（文案与各入口历史实现逐字节一致）；
// 成功时调用方必须 defer c.releasePriority(priority)。
func (c *Client) guardPriority(ctx context.Context) (Priority, error) {
	if c.priorityQueue == nil {
		return PriorityNormal, nil
	}
	priority := GetPriority(ctx)
	if !c.priorityQueue.Acquire(ctx, priority) {
		// #165：可分流哨兵 ErrPriorityUnavailable + ctx 取消原因一并 %w
		// （Acquire 仅在 ctx.Done 时返回 false——队列满会阻塞等待至取消），
		// errors.Is(err, ErrPriorityUnavailable) / errors.Is(err, context.Canceled)
		// 均可判定。文案前缀与历史逐字节一致（等价性基线见 exec_skeleton_test.go）。
		return PriorityNormal, fmt.Errorf("%s: %w (%w)", c.service, ErrPriorityUnavailable, ctx.Err())
	}
	return priority, nil
}

// releasePriority 释放 guardPriority 获取的许可（未配置队列时空操作）。
func (c *Client) releasePriority(priority Priority) {
	if c.priorityQueue != nil {
		c.priorityQueue.Release(priority)
	}
}

// doWithLBDone 执行传输层 Do 并回写负载均衡结果（#159/#162 收敛单源）：
// DoneInfo 以传输层最终错误回写（成功为 nil，Err 非 nil 即 P2C 摘除信号）；
// 传输层失败按 failVerb 包装——"request failed"/"upload failed" 各入口
// 历史文案保持不变。
func (c *Client) doWithLBDone(ctx context.Context, req *protocol.Request, resp *protocol.Response, lbDone loadbalance.DoneFunc, failVerb string) error {
	err := c.client.Do(ctx, req, resp)
	if lbDone != nil {
		lbDone(ctx, loadbalance.DoneInfo{Err: err})
	}
	if err != nil {
		return fmt.Errorf("%s: %s: %w", c.service, failVerb, err)
	}
	return nil
}

// exec 是 DoCodec/DoRaw/DoDetail/UploadFile/UploadBytes 五个封装方法共用
// 的请求执行骨架（#159 收敛单源）：分级准入守卫 → 寻址 → 协议对象获取/
// 释放 → 鉴权 → 请求体装载 → 传输（lbDone 回写）→ 响应处理。五个方法
// 收敛为薄封装，错误文案与外部行为保持历史逐字节等价
// （等价性回归基线见 exec_skeleton_test.go）。
//
// 参数约定：
//   - action：ServiceError.Action 文案（"GET /path" / "UPLOAD /path"）；
//   - failVerb：传输层失败的错误动词（"request failed"/"upload failed"）；
//   - buildBody：在准入守卫通过之后构建请求体（返回 data 与 Content-Type），
//     nil 或返回 (nil, "", nil) 表示不装载 body；错误即短路返回、不触网；
//   - out/cd 透传 handleResponse；两者均为 nil 时（DoRaw/DoDetail 形态）
//     返回值携带状态码与原始响应体——错误路径也带（DoDetail 语义），
//     DoRaw 在薄封装层按历史语义丢弃。
func (c *Client) exec(ctx context.Context, method, path, action, failVerb string, buildBody func() (data []byte, contentType string, err error), out any, cd codec.Codec) (int, []byte, error) {
	priority, err := c.guardPriority(ctx)
	if err != nil {
		return 0, nil, err
	}
	defer c.releasePriority(priority)

	fullURL, lbDone := c.resolveEndpoint(ctx, path)

	req, resp := protocol.AcquireRequest(), protocol.AcquireResponse()
	defer protocol.ReleaseRequest(req)
	defer protocol.ReleaseResponse(resp)

	req.SetMethod(method)
	req.SetRequestURI(fullURL)
	c.applyAuth(req)

	if buildBody != nil {
		data, contentType, berr := buildBody()
		if berr != nil {
			return 0, nil, berr
		}
		if data != nil || contentType != "" {
			req.SetBody(data)
			req.Header.SetContentTypeBytes([]byte(contentType))
		}
	}

	if err := c.doWithLBDone(ctx, req, resp, lbDone, failVerb); err != nil {
		return 0, nil, err
	}

	status := resp.StatusCode()
	var raw []byte
	if out == nil && cd == nil {
		raw = append([]byte(nil), resp.Body()...)
	}
	return status, raw, c.handleResponse(resp, action, out, cd)
}

// marshalJSONBody 构建 DoRaw/DoDetail 共用的 JSON 请求体装载器（body 为
// nil 时返回无 body 形态）。错误文案 "marshal body" 保持历史不变。
func marshalJSONBody(c *Client, body any) func() ([]byte, string, error) {
	return func() ([]byte, string, error) {
		if body == nil {
			return nil, "", nil
		}
		data, err := json.Marshal(body)
		if err != nil {
			return nil, "", fmt.Errorf("%s: marshal body: %w", c.service, err)
		}
		return data, "application/json", nil
	}
}

// DoCodec 使用指定编解码器发送请求：body 经 codec.Marshal 序列化并设置
// 对应 Content-Type；响应按其 Content-Type 反查 codec 反序列化到 out
// （查不到回退请求 codec）。codecName 未注册时返回 error 而非 panic（#114，
// 与 #83「失败显式返回、不升级为进程死亡」同族契约）：codec 名常来自配置，
// Must 语义只保留给显式调用 codec.MustGetCodec 的场景。
func (c *Client) DoCodec(ctx context.Context, codecName, method, path string, body any, out any) error {
	cd := codec.GetCodec(codecName)
	if cd == nil {
		// #165：可分流哨兵 ErrUnknownCodec（%w），errors.Is 单源判定，
		// 文案与历史逐字节一致。
		return fmt.Errorf("%s: %w %q", c.service, ErrUnknownCodec, codecName)
	}
	_, _, err := c.exec(ctx, method, path, method+" "+path, "request failed",
		func() ([]byte, string, error) {
			if body == nil {
				return nil, "", nil
			}
			data, err := cd.Marshal(body)
			if err != nil {
				return nil, "", fmt.Errorf("%s: marshal body: %w", c.service, err)
			}
			return data, cd.ContentType(), nil
		}, out, cd)
	return err
}

// DoRaw 使用指定的 HTTP 方法发送 JSON 请求，返回原始响应字节数据。
// 与 DoDetail 的语义差异（等价性基线钉住，不得合并）：非 2xx 时返回
// nil body + *ServiceError，仅成功时返回 body。
func (c *Client) DoRaw(ctx context.Context, method, path string, body any) ([]byte, error) {
	_, raw, err := c.exec(ctx, method, path, method+" "+path, "request failed",
		marshalJSONBody(c, body), nil, nil)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

// DoDetail 使用指定的 HTTP 方法发送请求，返回状态码与原始响应体。
// 与 DoJSON/DoRaw 不同：2xx 也返回原始 body（DoJSON 只把 body 解到 out，
// DoRaw 仅在成功时返回 body），非 2xx 同时返回 status、body 与
// *ServiceError，调用方可自行判定如何处理响应内容。
// 传输层失败时 status 为 0、body 为 nil。
func (c *Client) DoDetail(ctx context.Context, method, path string, body any) (int, []byte, error) {
	return c.exec(ctx, method, path, method+" "+path, "request failed",
		marshalJSONBody(c, body), nil, nil)
}

// UploadFile 上传本地文件 (multipart/form-data)：读取 filePath 全部内容
// 作为文件 part（filename 取 filepath.Base(filePath)——不带本地目录，
// 避免泄漏服务器目录结构），额外表单字段经 extraFields 透传（#86，重报 #102）。
// 内存中的字节用 UploadBytes。
func (c *Client) UploadFile(ctx context.Context, path, filePath, fieldName string, extraFields map[string]string, out any) error {
	_, _, err := c.exec(ctx, http.MethodPost, path, "UPLOAD "+path, "upload failed",
		func() ([]byte, string, error) {
			// 修复前（#86，重报 #102）本方法把 filePath 直接当 filename 传给 multipart 构造
			// 且 data 恒为 nil——上传请求成功发出但文件 part 内容恒为空（空文件），
			// 且 part filename 是完整本地路径（泄漏服务器目录结构）。
			data, err := os.ReadFile(filePath)
			if err != nil {
				return nil, "", fmt.Errorf("%s: read upload file: %w", c.service, err)
			}
			body, contentType, err := buildMultipartFileBytes(fieldName, filepath.Base(filePath), data, extraFields)
			if err != nil {
				return nil, "", fmt.Errorf("%s: build multipart: %w", c.service, err)
			}
			return body, contentType, nil
		}, out, nil)
	return err
}

// UploadBytes 上传字节数据 (multipart/form-data)。
func (c *Client) UploadBytes(ctx context.Context, path string, data []byte, fieldName, fileName string, extraFields map[string]string, out any) error {
	_, _, err := c.exec(ctx, http.MethodPost, path, "UPLOAD "+path, "upload failed",
		func() ([]byte, string, error) {
			body, contentType, err := buildMultipartFileBytes(fieldName, fileName, data, extraFields)
			if err != nil {
				return nil, "", fmt.Errorf("%s: build multipart: %w", c.service, err)
			}
			return body, contentType, nil
		}, out, nil)
	return err
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
