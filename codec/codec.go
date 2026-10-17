// Package codec 提供请求/响应编解码器的注册制抽象（参照 go-kratos encoding）。
//
// Client.DoCodec 按 codecName 序列化请求体、设置对应 Content-Type，
// 响应按 Content-Type 反查 codec 反序列化（查不到回退请求 codec）。
// json 内置注册；form/protobuf 等按需 RegisterCodec 扩展。
//
// 实现必须并发安全：Codec 的方法会被并发调用。
package codec

import (
	"fmt"
	"strings"
	"sync"
)

// DefaultName 是默认编解码器名称（json，包 init 时注册）。
const DefaultName = "json"

// Codec 是传输编解码接口。
type Codec interface {
	// Name 返回编解码器名称（注册键；调用方注册时会转小写）。
	// 返回值必须静态不变。
	Name() string
	// ContentType 返回对应的 HTTP Content-Type（如 application/json）。
	ContentType() string
	// Marshal 序列化 v 为线上格式。
	Marshal(v any) ([]byte, error)
	// Unmarshal 解析线上格式到 v。
	Unmarshal(data []byte, v any) error
}

var (
	mu     sync.RWMutex
	codecs = make(map[string]Codec)
)

// RegisterCodec 注册编解码器（按 Name() 小写归一）。nil 或空名 panic；
// 同名重复注册覆盖（测试与扩展场景）。
func RegisterCodec(c Codec) {
	if c == nil {
		panic("codec: cannot register a nil Codec")
	}
	if c.Name() == "" {
		panic("codec: cannot register a Codec with empty name")
	}
	mu.Lock()
	defer mu.Unlock()
	codecs[strings.ToLower(c.Name())] = c
}

// GetCodec 按名称（大小写不敏感）取已注册编解码器，未注册返回 nil。
func GetCodec(name string) Codec {
	mu.RLock()
	defer mu.RUnlock()
	return codecs[strings.ToLower(name)]
}

// MustGetCodec 按名称取编解码器，未注册 panic。
func MustGetCodec(name string) Codec {
	c := GetCodec(name)
	if c == nil {
		panic(fmt.Sprintf("codec: no codec registered for %q", name))
	}
	return c
}
