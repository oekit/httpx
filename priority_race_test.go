package httpx

import (
	"context"
	"sync"
	"testing"
	"time"
)

// 本文件补 PriorityQueue 并发面的 -race 用例（#171）：此前只有单线程的
// Release 未知优先级与 capacity panic 用例，并发 Acquire/Release + ctx 取消
// （Acquire false 后不泄漏许可）零覆盖。

// TestPriorityQueue_ConcurrentAcquireRelease 混合 high/normal/low/未知优先级
// 的并发 Acquire/Release + ctx 超时取消，跑 -race：许可账目必须收支平衡
// （结束Len()==0），取消路径不得泄漏或双释许可。
func TestPriorityQueue_ConcurrentAcquireRelease(t *testing.T) {
	q := NewPriorityQueue(4)
	priorities := []Priority{PriorityHigh, PriorityNormal, PriorityLow, Priority(99)}

	var wg sync.WaitGroup
	const workers = 24
	const iters = 50
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := priorities[w%len(priorities)]
			for range iters {
				// 短超时 ctx：容量 4 < workers 24，稳态饱和，大量 Acquire 走
				// ctx 取消分支（false），与成功路径交错形成竞争面
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Millisecond)
				if q.Acquire(ctx, p) {
					q.Release(p)
				}
				cancel()
			}
		}()
	}
	wg.Wait()

	if got := q.Len(); got != 0 {
		t.Errorf("Len() = %d after concurrent acquire/release, want 0（并发许可泄漏）", got)
	}
	// 收支平衡后容量完全恢复
	if !q.Acquire(context.Background(), PriorityNormal) {
		t.Error("Acquire after drain = false, want true（许可泄漏挤占容量）")
	}
	q.Release(PriorityNormal)
}

// TestPriorityQueue_AcquireCancelledCtx_NoLeak 回归（#171 附带）：队列饱和时
// 对已取消 ctx 的 Acquire 必须返回 false 且**不消耗许可**——修复前该路径
// 无测试；若 Acquire false 后调用方误 Release 会双释他人许可，若实现误占
// 则容量永久缩小。两条都由本用例钉住。
func TestPriorityQueue_AcquireCancelledCtx_NoLeak(t *testing.T) {
	q := NewPriorityQueue(1)
	if !q.Acquire(context.Background(), PriorityNormal) {
		t.Fatal("precondition: first acquire must succeed")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if q.Acquire(ctx, PriorityNormal) {
		t.Fatal("saturated queue with cancelled ctx must reject")
	}
	if got := q.Len(); got != 1 {
		t.Errorf("rejected Acquire mutated permit count: Len = %d, want 1", got)
	}

	// 正常释放后容量恢复，取消路径没有留下幽灵许可
	q.Release(PriorityNormal)
	if got := q.Len(); got != 0 {
		t.Errorf("Len() after release = %d, want 0", got)
	}
	if !q.Acquire(context.Background(), PriorityHigh) {
		t.Error("Acquire after release = false, want true（取消路径泄漏许可挤占容量）")
	}
	q.Release(PriorityHigh)
}
