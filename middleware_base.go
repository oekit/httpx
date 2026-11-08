package httpx

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/protocol"
)

// baseMiddleware 是请求补全层，注册在最外层，对全部请求路径生效——
// 包括调用方直接使用的 c.Hertz().Do()（历史上封装方法才有 baseURL/Basic
// auth/cookie jar，裸 Do 缺三样，曾在下游引发 5 处生产 bug）。
//
// 行为（全部幂等，不覆盖调用方已有设置）：
//   - 相对 URI 拼接 baseURL；绝对 URI 不动
//   - 无 Authorization 头时注入 Bearer/Basic（applyAuth）
//   - cookie jar：请求前注入匹配 Cookie，响应后回收 Set-Cookie
func (c *Client) baseMiddleware() client.Middleware {
	return func(next client.Endpoint) client.Endpoint {
		return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) error {
			if len(req.URI().Host()) == 0 && c.baseURL != "" {
				base := strings.TrimSuffix(c.baseURL, "/")
				path := string(req.URI().PathOriginal())
				if !strings.HasPrefix(path, "/") {
					path = "/" + path
				}
				target := base + path
				if q := string(req.URI().QueryString()); q != "" {
					target += "?" + q
				}
				req.SetRequestURI(target)
			}
			if len(req.Header.Get("Authorization")) == 0 {
				c.applyAuth(req)
			}

			var jarURL *url.URL
			if c.cookieJar != nil {
				if u, err := url.Parse(string(req.URI().FullURI())); err == nil {
					jarURL = u
					for _, ck := range c.cookieJar.Cookies(jarURL) {
						// 调用方已设同名 cookie 时跳过（#113）：SetCookie 走
						// setArg 同键替换，直接注入会静默改写调用方值——对齐
						// 本中间件「不覆盖调用方已有设置」的承诺
						if len(req.Header.Cookie(ck.Name)) > 0 {
							continue
						}
						req.Header.SetCookie(ck.Name, ck.Value)
					}
				}
			}

			err := next(ctx, req, resp)

			if jarURL != nil && resp != nil {
				if cookies := setCookiesFromResponse(resp); len(cookies) > 0 {
					c.cookieJar.SetCookies(jarURL, cookies)
				}
			}
			return err
		}
	}
}

// setCookiesFromResponse 把 Hertz 响应头的 Set-Cookie 解析成标准 Cookie。
func setCookiesFromResponse(resp *protocol.Response) []*http.Cookie {
	h := make(http.Header)
	resp.Header.VisitAllCookie(func(key, value []byte) {
		h.Add("Set-Cookie", string(value))
	})
	if len(h) == 0 {
		return nil
	}
	return (&http.Response{Header: h}).Cookies()
}
