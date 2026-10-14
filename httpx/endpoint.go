package httpx

import (
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

func (c *Client) newEndpointManager(resolver ServiceResolver, defaultInterval time.Duration) *EndpointManager {
	interval := defaultInterval
	if c.pendingRefreshInterval > 0 {
		interval = c.pendingRefreshInterval
	}
	return NewEndpointManager(resolver, c.pendingBalance, interval)
}

var reqPool = sync.Pool{
	New: func() any { return &http.Request{URL: &url.URL{}} },
}

func (c *Client) resolveEndpoint(path string) string {
	if c.endpointManager == nil {
		if c.baseURL != "" {
			return strings.TrimRight(c.baseURL, "/") + path
		}
		return path
	}

	req := reqPool.Get().(*http.Request)
	req.URL.Path = path
	endpoint := c.endpointManager.Pick(req)
	reqPool.Put(req)

	if endpoint == "" {
		if c.baseURL != "" {
			return strings.TrimRight(c.baseURL, "/") + path
		}
		return path
	}
	return endpoint + path
}
