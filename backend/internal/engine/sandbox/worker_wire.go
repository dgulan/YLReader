package sandbox

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strconv"
)

const (
	wireHello byte = iota + 1
	wireEval
	wireHTTP
	wireReply
	wireResult
	workerHTTPMax = 64
)

var errWorkerProtocol = errors.New("sandbox: invalid worker protocol")
var ErrWorkerUnavailable = errors.New("sandbox: isolated worker unavailable")

type workerHello struct {
	Profile   string `json:"profile"`
	MemoryMax int64  `json:"memory_max"`
}
type workerEval struct {
	Script     string         `json:"script"`
	Vars       map[string]any `json:"vars"`
	Structured bool           `json:"structured"`
}
type workerHTTP struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Body   []byte `json:"body"`
}
type workerReply struct {
	Body []byte `json:"body"`
	Code string `json:"code"`
}
type workerResult struct {
	OK    bool            `json:"ok"`
	Value json.RawMessage `json:"value,omitempty"`
	Code  string          `json:"code,omitempty"`
}
type workerFrame struct {
	Kind byte
	Seq  uint32
	Data []byte
}

func frameLimit(kind byte) uint32 {
	switch kind {
	case wireHello:
		return 1024
	case wireEval:
		return 40 << 20
	case wireHTTP:
		return 2 << 20
	case wireReply:
		return 24 << 20
	case wireResult:
		return 8 << 20
	}
	return 0
}
func readWorkerFrame(r io.Reader, allow func(byte, uint32) bool) (workerFrame, error) {
	var header [13]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return workerFrame{}, err
	}
	kind, seq, n := header[4], binary.BigEndian.Uint32(header[5:9]), binary.BigEndian.Uint32(header[9:13])
	if string(header[:4]) != "YLW1" || frameLimit(kind) == 0 || n == 0 || n > frameLimit(kind) || !allow(kind, seq) {
		return workerFrame{}, errWorkerProtocol
	}
	data := make([]byte, int(n))
	if _, err := io.ReadFull(r, data); err != nil {
		return workerFrame{}, err
	}
	return workerFrame{kind, seq, data}, nil
}
func writeWorkerFrame(w io.Writer, kind byte, seq uint32, v any) error {
	data, err := json.Marshal(v)
	if err != nil || len(data) == 0 || len(data) > int(frameLimit(kind)) {
		return errWorkerProtocol
	}
	var header [13]byte
	copy(header[:4], "YLW1")
	header[4] = kind
	binary.BigEndian.PutUint32(header[5:9], seq)
	binary.BigEndian.PutUint32(header[9:13], uint32(len(data)))
	for _, part := range [][]byte{header[:], data} {
		for len(part) > 0 {
			n, err := w.Write(part)
			if err != nil {
				return err
			}
			if n <= 0 {
				return io.ErrShortWrite
			}
			part = part[n:]
		}
	}
	return nil
}

func decodeWorkerJSON(data []byte, target any, required ...string) error {
	allowed := map[string]bool{}
	kinds := map[string]reflect.Kind{}
	t := reflect.TypeOf(target).Elem()
	for i := 0; i < t.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		for j, c := range tag {
			if c == ',' {
				tag = tag[:j]
				break
			}
		}
		allowed[tag] = true
		kinds[tag] = t.Field(i).Type.Kind()
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	count := 0
	root := map[string]bool{}
	var value func(int) error
	rootField := ""
	value = func(depth int) error {
		count++
		if depth > 40 || count > 262144 {
			return errWorkerProtocol
		}
		token, err := d.Token()
		if err != nil {
			return errWorkerProtocol
		}
		if depth == 1 && token == nil && kinds[rootField] != reflect.Map && kinds[rootField] != reflect.Slice {
			return errWorkerProtocol
		}
		if delim, ok := token.(json.Delim); ok {
			switch delim {
			case '{':
				seen := map[string]bool{}
				for d.More() {
					key, e := d.Token()
					name, ok := key.(string)
					if e != nil || !ok || seen[name] || (depth == 0 && !allowed[name]) {
						return errWorkerProtocol
					}
					if depth == 0 {
						rootField = name
					}
					seen[name] = true
					count++
					if count > 262144 {
						return errWorkerProtocol
					}
					if err := value(depth + 1); err != nil {
						return err
					}
				}
				if depth == 0 {
					root = seen
				}
				end, e := d.Token()
				if e != nil || end != json.Delim('}') {
					return errWorkerProtocol
				}
			case '[':
				if depth == 0 {
					return errWorkerProtocol
				}
				for d.More() {
					if err := value(depth + 1); err != nil {
						return err
					}
				}
				end, e := d.Token()
				if e != nil || end != json.Delim(']') {
					return errWorkerProtocol
				}
			default:
				return errWorkerProtocol
			}
		} else if depth == 0 {
			return errWorkerProtocol
		}
		return nil
	}
	if value(0) != nil {
		return errWorkerProtocol
	}
	if _, err := d.Token(); err != io.EOF {
		return errWorkerProtocol
	}
	for _, key := range required {
		if !root[key] {
			return errWorkerProtocol
		}
	}
	d = json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	d.DisallowUnknownFields()
	if d.Decode(target) != nil {
		return errWorkerProtocol
	}
	return nil
}

func wireNumbers(v any) (any, error) {
	switch value := v.(type) {
	case json.Number:
		if string(value) != "-0" {
			if n, err := strconv.ParseInt(string(value), 10, 64); err == nil {
				return n, nil
			}
		}
		f, err := strconv.ParseFloat(string(value), 64)
		if err != nil {
			return nil, errWorkerProtocol
		}
		return f, nil
	case map[string]any:
		for key, item := range value {
			next, err := wireNumbers(item)
			if err != nil {
				return nil, err
			}
			value[key] = next
		}
		return value, nil
	case []any:
		for i, item := range value {
			next, err := wireNumbers(item)
			if err != nil {
				return nil, err
			}
			value[i] = next
		}
		return value, nil
	default:
		return v, nil
	}
}

// jsonInputCopy removes methods, aliases and Go-backed mutable values before encoding.
// Call only after validateInput has established finite depth/node/byte bounds.
func jsonInputCopy(v reflect.Value) any {
	for v.IsValid() && v.Kind() == reflect.Interface {
		if v.IsNil() {
			return nil
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return nil
	}
	switch v.Kind() {
	case reflect.String:
		return v.String()
	case reflect.Bool:
		return v.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return v.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return v.Uint()
	case reflect.Float32, reflect.Float64:
		return v.Float()
	case reflect.Map:
		if v.IsNil() {
			return nil
		}
		out := make(map[string]any, v.Len())
		it := v.MapRange()
		for it.Next() {
			out[it.Key().String()] = jsonInputCopy(it.Value())
		}
		return out
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return nil
		}
		out := make([]any, v.Len())
		for i := range out {
			out[i] = jsonInputCopy(v.Index(i))
		}
		return out
	}
	return nil
}

func workerErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrSandboxTimeout):
		return "sandbox_timeout"
	case errors.Is(err, ErrSandboxOutputLimit):
		return "sandbox_output_limit"
	case errors.Is(err, ErrSandboxPanic):
		return "sandbox_panic"
	default:
		return "rule_execution_failed"
	}
}
func workerCodeError(code string) error {
	switch code {
	case "sandbox_timeout":
		return ErrSandboxTimeout
	case "sandbox_output_limit":
		return ErrSandboxOutputLimit
	case "sandbox_panic":
		return ErrSandboxPanic
	case "rule_execution_failed":
		return ErrRuleExecution
	}
	return errWorkerProtocol
}
