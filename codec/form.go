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
// 键名取 `form:"..."` tag（缺省用字段名）；嵌套结构与 map 不支持——
// form 语义本身是平铺 kv，需要嵌套请改用 json codec。
// slice 编码为重复键（k=v1&k=v2），解码同样接受逗号分隔。
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
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		key := f.Name
		if tag := f.Tag.Get("form"); tag != "" {
			parts := strings.Split(tag, ",")
			if parts[0] == "-" {
				continue
			}
			key = parts[0]
		}
		fv := rv.Field(i)
		if fv.Kind() == reflect.Slice {
			for j := 0; j < fv.Len(); j++ {
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
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if !f.IsExported() {
			continue
		}
		key := f.Name
		if tag := f.Tag.Get("form"); tag != "" {
			parts := strings.Split(tag, ",")
			if parts[0] == "-" {
				continue
			}
			key = parts[0]
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

func setSlice(fv reflect.Value, raws []string) error {
	slice := reflect.MakeSlice(fv.Type(), len(raws), len(raws))
	for i, raw := range raws {
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
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("parse int %q: %w", raw, err)
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(raw, 10, 64)
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
