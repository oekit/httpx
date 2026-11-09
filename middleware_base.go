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

// staticHeaderMiddleware 把 WithHeaders/WithHeader 声明的静态自定义头逐请求
// 注入（#128）：每个 k/v 一律 SetHeader（无条件 Set，覆盖请求上已设的同名值）。
//
// 注册位次（NewClient）：紧跟 base 中间件、先于用户 OnBeforeRequest 闭包——
//   - base 先补 URI 与鉴权头，静态头随后注入 ⇒ 声明了 Authorization 的静态头
//     覆盖 Bearer/Basic（API-Key 形态鉴权的目标场景，见 WithHeaders 文档）；
//   - 用户闭包在其后（内层）执行 ⇒ 同名键以闭包为准（静态兜底、动态覆盖）。
func (c *Client) staticHeaderMiddleware() client.Middleware {
	return func(next client.Endpoint) client.Endpoint {
		return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) error {
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			return next(ctx, req, resp)
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
