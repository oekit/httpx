package codec

import (
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"strconv"
	"strings"
)

// FormCodec 是 application/x-www-form-urlencoded 编解码器（#7）。
//
// 边界（明示）：仅支持**扁平**结构体字段（string/数值/bool/slice-of-scalar），
// 键名取 `form:"..."` tag（缺省用字段名；`form:",omitempty"` 这类空名 tag
// 同样回退字段名，#111）；嵌套结构与 map 不支持——
// form 语义本身是平铺 kv，需要嵌套请改用 json codec。
// slice 编码为重复键（k=v1&k=v2）；解码同时接受重复键与逗号分隔
// （k=a,b,c → [a b c]，#110）——两种形态可并存（k=1,2&k=3 → [1 2 3]）。
// 注意：元素本身含逗号的 string slice 无法与逗号形态区分（本仓 Encode 会
// 转义、ParseQuery 又还原成 ","），该形态请改用 json codec。
type FormCodec struct{}

func (FormCodec) Name() string        { return "form" }
func (FormCodec) ContentType() string { return "application/x-www-form-urlencoded" }

func (FormCodec) Marshal(v any) ([]byte, error) {
	vals, err := encodeForm(v)
	if err != nil {
		return nil, err
	}
	return []byte(vals.Encode()), nil
}

func (FormCodec) Unmarshal(data []byte, v any) error {
	vals, err := url.ParseQuery(string(data))
	if err != nil {
		return fmt.Errorf("form: parse query: %w", err)
	}
	return decodeForm(vals, v)
}

func init() {
	RegisterCodec(FormCodec{})
}

var errUnsupportedFormKind = errors.New("form: unsupported field type (flat scalars/slices only)")

func encodeForm(v any) (url.Values, error) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, errors.New("form: cannot marshal nil pointer")
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		// 非 struct：退回单值编码（string/数值）
		vals := url.Values{}
		s, err := scalarToString(rv)
		if err != nil {
			return nil, err
		}
		vals.Set("value", s)
		return vals, nil
	}
	vals := url.Values{}
	rt := rv.Type()
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		key, skip := formTagKey(f)
		if skip {
			continue
		}
		fv := rv.Field(i)
		if fv.Kind() == reflect.Slice {
			for j := range fv.Len() {
				s, err := scalarToString(fv.Index(j))
				if err != nil {
					return nil, fmt.Errorf("form: field %s: %w", f.Name, err)
				}
				vals.Add(key, s)
			}
			continue
		}
		s, err := scalarToString(fv)
		if err != nil {
			return nil, fmt.Errorf("form: field %s: %w", f.Name, err)
		}
		vals.Set(key, s)
	}
	return vals, nil
}

func decodeForm(vals url.Values, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return errors.New("form: Unmarshal target must be non-nil pointer")
	}
	rv = rv.Elem()
	if rv.Kind() != reflect.Struct {
		return decodeScalarForm(vals, rv)
	}
	rt := rv.Type()
	for i := range rt.NumField() {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		key, skip := formTagKey(f)
		if skip {
			continue
		}
		raws, ok := vals[key]
		if !ok || len(raws) == 0 {
			continue // 缺键保持零值
		}
		fv := rv.Field(i)
		if fv.Kind() == reflect.Slice {
			if err := setSlice(fv, raws); err != nil {
				return fmt.Errorf("form: field %s: %w", f.Name, err)
			}
			continue
		}
		if err := setScalar(fv, raws[0]); err != nil {
			return fmt.Errorf("form: field %s: %w", f.Name, err)
		}
	}
	return nil
}

// formTagKey 解析 `form:"..."` tag：返回键名与是否跳过该字段。首段 "-" 跳过；
// 首段为空（Go 标准库风格 `form:",omitempty"`，#111）回退字段名——修复前空
// 首段把键名置成空串：编码出 "=v" 无名参数、解码侧用空键查值恒零值。
func formTagKey(f reflect.StructField) (key string, skip bool) {
	key = f.Name
	if tag := f.Tag.Get("form"); tag != "" {
		parts := strings.Split(tag, ",")
		if parts[0] == "-" {
			return "", true
		}
		if parts[0] != "" {
			key = parts[0]
		}
	}
	return key, false
}

func setSlice(fv reflect.Value, raws []string) error {
	// 逗号分隔形态（#110）：外部表单 API 常输出 k=a,b,c，逐值按 "," 切分
	// （TrimSpace）后再解码；与重复键形态并存（k=1,2&k=3 → [1 2 3]）。
	// 修复前注释承诺「解码同样接受逗号分隔」但实现从不切分，逗号串被整体
	// 塞进单个元素。
	var elems []string
	for _, raw := range raws {
		for _, part := range strings.Split(raw, ",") {
			elems = append(elems, strings.TrimSpace(part))
		}
	}
	slice := reflect.MakeSlice(fv.Type(), len(elems), len(elems))
	for i, raw := range elems {
		if err := setScalar(slice.Index(i), raw); err != nil {
			return err
		}
	}
	fv.Set(slice)
	return nil
}

func setScalar(fv reflect.Value, raw string) error {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(raw)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("parse bool %q: %w", raw, err)
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		// 按目标字段位宽解析（#109）：64 位解析后 SetInt 对越界值**静默截断**
		// （300 写进 int8 变 44），必须按位宽报错——与 encoding/json 的
		// 「cannot unmarshal 300 into int8」语义对齐
		n, err := strconv.ParseInt(raw, 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("parse int %q: %w", raw, err)
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, fv.Type().Bits())
		if err != nil {
			return fmt.Errorf("parse uint %q: %w", raw, err)
		}
		fv.SetUint(n)
	case reflect.Float32, reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("parse float %q: %w", raw, err)
		}
		fv.SetFloat(f)
	default:
		return errUnsupportedFormKind
	}
	return nil
}

func scalarToString(rv reflect.Value) (string, error) {
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return "", nil
		}
		rv = rv.Elem()
	}
	switch rv.Kind() {
	case reflect.String:
		return rv.String(), nil
	case reflect.Bool:
		return strconv.FormatBool(rv.Bool()), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(rv.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(rv.Uint(), 10), nil
	case reflect.Float32:
		return strconv.FormatFloat(rv.Float(), 'f', -1, 32), nil
	case reflect.Float64:
		return strconv.FormatFloat(rv.Float(), 'f', -1, 64), nil
	default:
		return "", errUnsupportedFormKind
	}
}

func decodeScalarForm(vals url.Values, rv reflect.Value) error {
	raws, ok := vals["value"]
	if !ok || len(raws) == 0 {
		return nil
	}
	return setScalar(rv, raws[0])
}
