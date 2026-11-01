package serviceprovision

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"unicode/utf8"
)

// fileは先頭{、末尾}と任意の単一LFだけ。control/1 JSONLとは別の固定候補形式。
func strictRecord(data []byte) bool {
	if len(data) < 2 || len(data) > RecordBytes || !utf8.Valid(data) || !recordUnicode(data) || data[0] != '{' {
		return false
	}
	body := data
	if body[len(body)-1] == '\n' {
		body = body[:len(body)-1]
	}
	if len(body) < 2 || body[len(body)-1] != '}' {
		return false
	}
	d := json.NewDecoder(bytes.NewReader(body))
	d.UseNumber()
	value, err := recordValue(d, 1)
	if err != nil {
		return false
	}
	if _, err = d.Token(); err != io.EOF {
		return false
	}
	return recordShape(value, reflect.TypeOf(wireRecord{}))
}
func recordValue(d *json.Decoder, depth int) (any, error) {
	if depth > RecordDepth {
		return nil, ErrRecord
	}
	token, err := d.Token()
	if err != nil {
		return nil, ErrRecord
	}
	delim, container := token.(json.Delim)
	if !container {
		return token, nil
	}
	switch delim {
	case '{':
		values := make(map[string]any)
		for d.More() {
			token, err := d.Token()
			if err != nil {
				return nil, ErrRecord
			}
			name, ok := token.(string)
			if !ok {
				return nil, ErrRecord
			}
			if _, ok := values[name]; ok {
				return nil, ErrRecord
			}
			value, err := recordValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			values[name] = value
		}
		end, err := d.Token()
		if err != nil || end != json.Delim('}') {
			return nil, ErrRecord
		}
		return values, nil
	case '[':
		values := make([]any, 0)
		for d.More() {
			value, err := recordValue(d, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		end, err := d.Token()
		if err != nil || end != json.Delim(']') {
			return nil, ErrRecord
		}
		return values, nil
	}
	return nil, ErrRecord
}

// 型付きwireのfieldをexact-caseで照合する。helperSupplierだけは省略可能だが、
// 存在する場合はnullを許さず全内部fieldを必須にする。未知fieldは常に拒否する。
func recordShape(value any, typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Struct:
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		seen := 0
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name, options, _ := strings.Cut(field.Tag.Get("json"), ",")
			v, ok := object[name]
			if !ok && options == "omitempty" && typ == reflect.TypeOf(wireRecord{}) && name == "helperSupplier" {
				continue
			}
			if !ok || !recordShape(v, field.Type) {
				return false
			}
			seen++
		}
		return len(object) == seen
	case reflect.Pointer:
		return value != nil && recordShape(value, typ.Elem())
	case reflect.Map:
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		for _, v := range object {
			if !recordShape(v, typ.Elem()) {
				return false
			}
		}
		return true
	case reflect.Slice:
		values, ok := value.([]any)
		if !ok {
			return false
		}
		for _, v := range values {
			if !recordShape(v, typ.Elem()) {
				return false
			}
		}
		return true
	case reflect.String:
		_, ok := value.(string)
		return ok
	case reflect.Bool:
		_, ok := value.(bool)
		return ok
	}
	return false
}

// encoding/jsonの不正surrogate置換で、固定path等を別の文字列へ変えません。
// 通常のUTF8文字と、正しく対になったUnicode escapeはそのまま許します。
func recordUnicode(data []byte) bool {
	for i := 0; i < len(data); i++ {
		if data[i] != '\\' {
			continue
		}
		if i+1 >= len(data) {
			return false
		}
		if data[i+1] != 'u' {
			i++
			continue
		}
		if i+5 >= len(data) {
			return false
		}
		value, err := strconv.ParseUint(string(data[i+2:i+6]), 16, 16)
		if err != nil {
			return false
		}
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+11 >= len(data) || data[i+6] != '\\' || data[i+7] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(data[i+8:i+12]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 11
		} else {
			i += 5
		}
	}
	return true
}
