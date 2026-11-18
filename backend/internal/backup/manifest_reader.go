package backup

import (
	"encoding/json"
	"io"
)

type jsonFrame struct {
	open  byte
	bytes int64
	limit int64
}

// manifestReader bounds JSON values BEFORE Decoder can allocate an enormous
// RawMessage or string. Only the top-level objects array may span the document.
type manifestReader struct {
	whitespaceBytes                              int
	reader                                       io.Reader
	stack                                        []jsonFrame
	inString, escape, keyString, rootKeyExpected bool
	stringBytes, scalarBytes                     int
	key                                          []byte
	rootKey                                      string
	exceeded                                     bool
	invalid                                      bool
	documentLimit                                int64
}

func (r *manifestReader) Read(p []byte) (int, error) {
	if r.invalid {
		return 0, ErrPackageSchema
	}
	if r.exceeded {
		return 0, ErrPackageLimit
	}
	if len(p) > 4096 {
		p = p[:4096]
	}
	n, e := r.reader.Read(p)
	for _, b := range p[:n] {
		if !r.scan(b) {
			if r.invalid {
				return 0, ErrPackageSchema
			}
			r.exceeded = true
			return 0, ErrPackageLimit
		}
	}
	return n, e
}
func (r *manifestReader) scan(b byte) bool {
	for i := range r.stack {
		r.stack[i].bytes++
		if r.stack[i].bytes > r.stack[i].limit {
			return false
		}
	}
	if r.inString {
		r.stringBytes++
		if r.stringBytes > 16<<10 {
			return false
		}
		if r.keyString {
			r.key = append(r.key, b)
		}
		if r.escape {
			r.escape = false
			return true
		}
		if b == '\\' {
			r.escape = true
			return true
		}
		if b == '"' {
			r.inString = false
			if r.keyString {
				if json.Unmarshal(r.key, &r.rootKey) != nil {
					r.rootKey = ""
				}
				r.rootKeyExpected = false
			}
		}
		return true
	}
	if b == ' ' || b == '\t' || b == '\r' || b == '\n' {
		r.whitespaceBytes++
		if r.whitespaceBytes > 16<<10 {
			return false
		}
	} else {
		r.whitespaceBytes = 0
	}
	switch b {
	case '"':
		r.inString = true
		r.stringBytes = 0
		r.scalarBytes = 0
		r.keyString = len(r.stack) == 1 && r.rootKeyExpected
		if r.keyString {
			r.key = append(r.key[:0], '"')
		}
	case '{', '[':
		r.scalarBytes = 0
		if len(r.stack) >= 8 {
			return false
		}
		limit := int64(16 << 10)
		if len(r.stack) == 0 {
			limit = r.documentLimit
			r.rootKeyExpected = true
		} else if len(r.stack) == 1 {
			limit = 1 << 20
			if b == '[' && r.rootKey == "objects" {
				limit = r.documentLimit
			}
		}
		r.stack = append(r.stack, jsonFrame{open: b, bytes: 1, limit: limit})
	case '}', ']':
		r.scalarBytes = 0
		if len(r.stack) == 0 || (b == '}' && r.stack[len(r.stack)-1].open != '{') || (b == ']' && r.stack[len(r.stack)-1].open != '[') {
			r.invalid = true
			return false
		}
		r.stack = r.stack[:len(r.stack)-1]
	case ',':
		r.scalarBytes = 0
		if len(r.stack) == 1 {
			r.rootKeyExpected = true
			r.rootKey = ""
		}
	case ':', ' ', '\n', '\r', '\t':
		r.scalarBytes = 0
	default:
		r.scalarBytes++
		if r.scalarBytes > 128 {
			return false
		}
	}
	return true
}
