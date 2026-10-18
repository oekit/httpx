package httpx

import (
	"testing"

	"github.com/cloudwego/hertz/pkg/network"
	"github.com/oekit/httpx/urlguard"
)

func TestSafeDialFunc_BlocksBeforeNext(t *testing.T) {
	called := false
	fn := SafeDialFunc(func(addr string) (network.Conn, error) {
		called = true
		return nil, nil
	})
	if _, err := fn("127.0.0.1:80"); err == nil {
		t.Fatal("expected block")
	}
	if called {
		t.Fatal("next must not be called")
	}
}

func TestWithSafeDial_OptionPresent(t *testing.T) {
	c := &Client{}
	WithSafeDial()(c)
	if len(c.hzOpts) == 0 {
		t.Fatal("WithSafeDial should add hz option")
	}
}

func TestURLGuard_Hook(t *testing.T) {
	if err := urlguard.IsBlockedURL("https://10.0.0.1/h"); err == nil {
		t.Fatal("expected block")
	}
	if err := urlguard.IsBlockedURL("https://example.com/h"); err != nil {
		t.Fatalf("expected pass, got %v", err)
	}
}
