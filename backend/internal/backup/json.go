package backup

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// exactJSON enforces required, case-sensitive field names, duplicate/null rules
// recursively before typed decoding. Decoder errors never escape with secrets.
func exactJSON(data []byte, out any) error {
	if len(data) > 1<<20 || !utf8.Valid(data) || !pairedUnicodeEscapes(data) {
		return ErrConfig
	}
	if e := exactValue(data, reflect.TypeOf(out).Elem()); e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return ErrConfig
	}
	return nil
}

// encoding/json replaces unpaired UTF-16 escapes with U+FFFD. Reject those
// inputs rather than silently changing an object key or a secret-file path.
func pairedUnicodeEscapes(data []byte) bool {
	inside := false
	for i := 0; i < len(data); i++ {
		if data[i] == '"' {
			inside = !inside
			continue
		}
		if !inside || data[i] != '\\' {
			continue
		}
		i++
		if i >= len(data) {
			return false
		}
		if data[i] != 'u' {
			continue
		}
		if i+5 > len(data) {
			return false
		}
		value, e := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if e != nil {
			return false
		}
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+7 > len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return false
			}
			low, e := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if e != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func exactValue(data []byte, t reflect.Type) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return ErrConfig
	}
	if t == reflect.TypeOf(time.Time{}) {
		var stamp string
		if json.Unmarshal(data, &stamp) != nil || !utcText.MatchString(stamp) {
			return ErrConfig
		}
		return nil
	}
	if t.Kind() == reflect.Struct {
		d := json.NewDecoder(bytes.NewReader(data))
		token, e := d.Token()
		if e != nil || token != json.Delim('{') {
			return ErrConfig
		}
		fields := map[string]reflect.StructField{}
		required := map[string]bool{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			parts := strings.Split(f.Tag.Get("json"), ",")
			name := parts[0]
			if name == "" || name == "-" {
				continue
			}
			fields[name] = f
			if len(parts) < 2 || parts[1] != "omitempty" {
				required[name] = true
			}
		}
		seen := map[string]bool{}
		for d.More() {
			token, e = d.Token()
			key, ok := token.(string)
			f, known := fields[key]
			if e != nil || !ok || !known || seen[key] {
				return ErrConfig
			}
			seen[key] = true
			var raw json.RawMessage
			if d.Decode(&raw) != nil || exactValue(raw, f.Type) != nil {
				return ErrConfig
			}
			if strings.Contains(f.Tag.Get("json"), ",omitempty") && bytes.Equal(raw, []byte(`""`)) {
				return ErrConfig
			}
		}
		if _, e = d.Token(); e != nil || d.Decode(new(any)) != io.EOF {
			return ErrConfig
		}
		for key := range required {
			if !seen[key] {
				return ErrConfig
			}
		}
		return nil
	}
	if t.Kind() == reflect.Slice {
		var values []json.RawMessage
		if json.Unmarshal(data, &values) != nil {
			return ErrConfig
		}
		for _, raw := range values {
			if exactValue(raw, t.Elem()) != nil {
				return ErrConfig
			}
		}
		return nil
	}
	// Typed decoding below rejects non-integer numbers and wrong primitive types.
	return nil
}
