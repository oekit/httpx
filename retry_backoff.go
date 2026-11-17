package httpx

import (
	"context"
	"math"
	"math/rand/v2"
	"time"

	"github.com/cloudwego/hertz/pkg/app/client"
	"github.com/cloudwego/hertz/pkg/protocol"
	retryhttpx "github.com/oekit/concurrency/retry/httpx"
	"github.com/oekit/pkg/contextx"
	"github.com/oekit/pkg/timex"
)

// maxRetryAttempts 是 WithRetryConfig 允许的 maxAttempts 上限（#236）。
//
// 档位是指数增长的（initDelay<<attempts）：无上限的尝试次数配合 0 封顶会把
// 退避推到不可用的量级——WithRetryConfig(40, 100ms, 0, 0) 在 attempts≥40 时
// `100ms<<40` 直接溢出 int64（hertz 侧经 CombineDelay 的 uint64 转换被钳成
// ≈292 年的等待）。10 次尝试（9 次重试）覆盖绝大多数真实重试策略，配合
// 溢出饱和与可中断等待构成三重安全网。
const maxRetryAttempts = 10

// retryPolicy 是 WithRetryConfig 的延迟参数快照（#236）：退避计算与等待都由
// httpx 层执行，hertz 侧只保留尝试次数上限。
type retryPolicy struct {
	maxAttempts uint
	initDelay   time.Duration
	maxDelay    time.Duration
	maxJitter   time.Duration
}

// delay 计算第 attempts 次重试前的等待时长（attempts 从 1 起，与 hertz
// BackOffDelayPolicy 的 initDelay<<attempts 同口径：首次重试 = 2×initDelay）。
//
// hint 是本次响应 Retry-After 的服务端建议（hasHint=false 时忽略）（#234）：
// 有建议时**覆盖**客户端计算值（含抖动）——服务端对本次限流窗口的直接指令，
// 客户端退避参数只是它的回退；maxDelay 封顶对建议同样生效（与矩阵单源
// concurrency/retry 的 DelayHint 口径一致：建议值仍受 MaxDelay 封顶）。
//
// 与 hertz retry.Delay 的差异（#236）：
//   - 指数档位**溢出饱和**到 MaxInt64 而非回绕：hertz 的 `Delay << attempts`
//     溢出成负值后，CombineDelay 里 `uint64(负值)` 变成天文数字、被钳到
//     MaxInt64（≈292 年），退避直接退化成挂死；
//   - maxDelay <= 0 是**显式不封顶**（WithRetryConfig godoc 承诺的语义），
//     只在 >0 时封顶；
//   - 抖动 [0, maxJitter) 与 hertz RandomDelayPolicy 同口径（maxJitter <= 0
//     不抖），且加法同样饱和，不会把接近 MaxInt64 的退避加成负值。
func (p *retryPolicy) delay(attempts uint, hint time.Duration, hasHint bool) time.Duration {
	var d time.Duration
	// 负建议视为无效（ParseRetryAfter 不会产出负值，防御下游自造 hint）
	if hasHint && hint >= 0 {
		d = hint // 服务端建议直接定档：不再叠加客户端计算值与抖动
	} else {
		d = p.backoff(attempts)
		if p.maxJitter > 0 {
			span := int64(p.maxJitter)
			if d > time.Duration(math.MaxInt64)-time.Duration(span) {
				d = time.Duration(math.MaxInt64)
			} else {
				d += time.Duration(rand.Int64N(span))
			}
		}
	}
	if p.maxDelay > 0 && d > p.maxDelay {
		return p.maxDelay
	}
	return d
}

// retryAfterHint 读取响应的 Retry-After 建议（#234）：delay-seconds 与
// HTTP-date 双形态，解析复用矩阵单源 concurrency/retry/httpx.ParseRetryAfter
// （本仓不再写一份头解析）。未携带/空串/坏值返回 (0, false) 走计算退避；
// Retry-After: 0 是合法建议（立即重试），返回 (0, true) 与「未携带」可区分。
func retryAfterHint(resp *protocol.Response) (time.Duration, bool) {
	if resp == nil {
		return 0, false
	}
	return retryhttpx.ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now())
}

// backoff 指数档位 initDelay<<attempts，溢出饱和（见 delay 注释）。
func (p *retryPolicy) backoff(attempts uint) time.Duration {
	if p.initDelay <= 0 {
		return 0
	}
	// 与 hertz BackOffDelayPolicy 一致把档位钳到 62：再往后必然溢出。
	if attempts > 62 {
		attempts = 62
	}
	d := p.initDelay
	for i := uint(0); i < attempts; i++ {
		if d > time.Duration(math.MaxInt64)/2 {
			return time.Duration(math.MaxInt64)
		}
		d *= 2
	}
	return d
}

// waitRetry 执行一次重试退避等待（#236）：**可被 ctx 取消/超时打断**。
//
// hertz 的重试循环以裸 time.Sleep 等待退避、只在循环顶部查 ctx.Done——退避
// 期间取消不生效，最坏要睡满当前档（WithRetryConfig(8, 1s, 0, 0) 的第 7 档
// 64s；无封顶时更长），单请求总时长被放大到分钟级，与 WithTimeout/WithStream
// 反复强调的「总时长预算由调用方经 ctx 控制」直接冲突。
//
// 可中断 sleep 已单源下沉 timex.SleepContext（#530，语义与本函数原实现逐字
// 一致：d <= 0 直通不查取消、ctx 取消/超时立即返回 ctx.Err()、计时走
// time.NewTimer——testing/synctest 假时钟可完全接管，确定性时序测试不睡真
// 时间，见 retry_backoff_test.go），此处薄委托。返回的 ctx.Err() 不需要：
// 取消后 hertz 循环顶部的 ctx.Done 分支以 ctx.Err() 结束本次请求（谓词仍
// 返回「要重试」——返回 false 会让已取消的请求以最后一轮的响应/传输错误
// 收尾，调用方拿不到取消原因）。
// nil-ctx 兜底经 contextx.OrBackground 归一（#529 单源；SleepContext 内部
// 同口径兜底，显式归一保持原貌）。
func waitRetry(ctx context.Context, d time.Duration) {
	_ = timex.SleepContext(contextx.OrBackground(ctx), d)
}

// retryScope 承载单次请求在退避期需要、而 hertz 重试谓词签名
// (req, resp, err) 装不下的两样东西：ctx（退避可中断）与 attempts（指数退避
// 当前档位）。
//
// 只被该请求自己的 goroutine 读写（HostClient.Do 的重试循环与中间件链同
// goroutine），故字段无需原子；跨请求的并发落在 retryScopes 这张 sync.Map 上。
type retryScope struct {
	ctx      context.Context
	attempts uint
}

// retryScopeMiddleware 登记本次请求的退避作用域（#236）。
//
// 为什么必须有它：hertz 的 RetryIfFunc 签名里没有 ctx，而退避发生在谓词
// 调用之后的同一 goroutine 上——把 ctx 绑到 *protocol.Request（一次 Do 独占
// 持有、进出一一配对），谓词即可取回它做可中断等待并自数 attempts 档位。
//
// 注册在**整条链最外层**（首个 Use），保证所有入口——封装方法、net/http 桥接
// 层、直接 Hertz().Do——都登记；defer Delete 保证不泄漏（含 panic 路径）。
func (c *Client) retryScopeMiddleware() client.Middleware {
	return func(next client.Endpoint) client.Endpoint {
		return func(ctx context.Context, req *protocol.Request, resp *protocol.Response) error {
			c.retryScopes.Store(req, &retryScope{ctx: ctx})
			defer c.retryScopes.Delete(req)
			return next(ctx, req, resp)
		}
	}
}

// takeRetryScope 取回并前进本次请求的退避档位：attempts 从 1 起（与 hertz
// 调谓词前的 attempts++ 同步——谓词只在还有重试预算时被调用，故每次调用前进
// 一档即可对齐）。scope 缺失（防御分支：登记中间件必达，理论不可达）时退回
// 「Background ctx + 首档」，至少不退化成零等待紧循环。
func (c *Client) takeRetryScope(req *protocol.Request) (context.Context, uint) {
	v, ok := c.retryScopes.Load(req)
	if !ok || v == nil {
		return context.Background(), 1
	}
	s := v.(*retryScope)
	s.attempts++
	return s.ctx, s.attempts
}
