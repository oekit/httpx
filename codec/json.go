package codec

import "encoding/json"

// JSONCodec 是内置 JSON 编解码器（encoding/json），与 v0.3.x GetJSON
// 的序列化行为一致。
type JSONCodec struct{}

func (JSONCodec) Name() string        { return DefaultName }
func (JSONCodec) ContentType() string { return "application/json" }

func (JSONCodec) Marshal(v any) ([]byte, error) { return json.Marshal(v) }

func (JSONCodec) Unmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

func init() {
	RegisterCodec(JSONCodec{})
}
