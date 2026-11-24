package httpx

import (
	"testing"
	"time"

	prom "github.com/prometheus/client_golang/prometheus"
)

// TestRequestDurationBucketsCoverDefaultTimeout 回归（#238）：
// request_duration_seconds 的桶上界必须覆盖缺省读超时 30s——缺省用
// prom.DefBuckets（最大有限桶 10s）时，10s~30s 的慢请求与超时全部落 +Inf，
// histogram_quantile 的 P95/P99 恒 ≤10s，长尾不可分辨、SLO 既证不了也证伪不了。
func TestRequestDurationBucketsCoverDefaultTimeout(t *testing.T) {
	reg := prom.NewRegistry()
	c := MustNewClient("bucket-svc", WithMetrics(reg, ""))
	t.Cleanup(c.Close)

	// 直方图子样本在首次 Observe 时才创建；25s 落在 (10s, 30s] 区间——
	// 缺省 DefBuckets 形态下它只能进 +Inf，本断言正是要钉住它有有限归属桶。
	c.metrics.requestDuration.WithLabelValues("bucket-svc", defaultTargetLabel, "GET").
		Observe((25 * time.Second).Seconds())

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	var buckets []float64
	var infCount uint64
	for _, mf := range mfs {
		if mf.GetName() != "http_client_request_duration_seconds" {
			continue
		}
		for _, m := range mf.GetMetric() {
			h := m.GetHistogram()
			if h == nil {
				continue
			}
			for _, b := range h.GetBucket() {
				if b.GetCumulativeCount() == 0 {
					continue
				}
				if b.GetUpperBound() > 1e300 { // +Inf
					infCount = b.GetCumulativeCount()
					continue
				}
				buckets = append(buckets, b.GetUpperBound())
			}
		}
	}
	if len(buckets) == 0 {
		t.Fatal("25s 样本全落 +Inf、无有限桶归属——桶上界不足 30s（#238）")
	}
	max := buckets[0]
	for _, b := range buckets {
		if b > max {
			max = b
		}
	}
	if max < 30 {
		t.Fatalf("最大有限桶上界 = %vs，want ≥ 30s（缺省读超时 30s，#238）", max)
	}
	if infCount != 0 {
		t.Fatalf("25s 样本落进了 +Inf（有限桶上界不足），+Inf count = %d", infCount)
	}
}
