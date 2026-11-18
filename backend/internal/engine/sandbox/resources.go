package sandbox

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"unicode/utf8"

	"github.com/dop251/goja"
)

// This budgets exported/input JSON, not the live JS heap or native allocations.
type valueBudget struct {
	ctx          context.Context
	bytes, nodes int
}

func (b *valueBudget) take(n int) error {
	if b.ctx.Err() != nil {
		return ErrSandboxTimeout
	}
	if n < 0 || n > b.bytes {
		return ErrSandboxOutputLimit
	}
	b.bytes -= n
	return nil
}
func (b *valueBudget) node(depth int) error {
	if depth > DefaultValueDepth || b.nodes <= 0 {
		return ErrSandboxOutputLimit
	}
	b.nodes--
	return b.take(0)
}
func (b *valueBudget) text(s string) error {
	if len(s) > b.bytes {
		return ErrSandboxOutputLimit
	}
	if err := b.take(2); err != nil {
		return err
	}
	remaining, lastCheck := b.bytes, 0
	for i := 0; i < len(s); {
		if i-lastCheck >= 4096 {
			if b.ctx.Err() != nil {
				return ErrSandboxTimeout
			}
			lastCheck = i
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		cost := size
		switch {
		case r == '"' || r == '\\':
			cost = 2
		case r < 0x20 || r == '<' || r == '>' || r == '&' || r == 0x2028 || r == 0x2029 || (r == utf8.RuneError && size == 1):
			cost = 6
		}
		remaining -= cost
		if remaining < 0 {
			return ErrSandboxOutputLimit
		}
		i += size
	}
	b.bytes = remaining
	return nil
}
func (b *valueBudget) scalar(v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return ErrRuleExecution
	}
	return b.take(len(raw))
}

// validateInput returns the conservative JSON-encoded size of vars so the
// caller can charge parent in-flight admission. The size is an estimate used
// only for budgeting; frame limits remain authoritative.
func validateInput(ctx context.Context, vars map[string]any, limit int) (int, error) {
	if len(vars) > 1024 {
		return 0, ErrRuleExecution
	}
	b := &valueBudget{ctx: ctx, bytes: limit, nodes: DefaultValueNodes}
	err := b.input(reflect.ValueOf(vars), 0)
	if err != nil && err != ErrSandboxTimeout {
		return 0, ErrRuleExecution
	}
	if err != nil {
		return 0, err
	}
	return limit - b.bytes, nil
}
func (b *valueBudget) input(v reflect.Value, depth int) error {
	if err := b.node(depth); err != nil {
		return err
	}
	for v.IsValid() && v.Kind() == reflect.Interface {
		if v.IsNil() {
			return b.take(4)
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return b.take(4)
	}
	switch v.Kind() {
	case reflect.String:
		return b.text(v.String())
	case reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
		return b.scalar(v.Interface())
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return ErrRuleExecution
		}
		if v.IsNil() {
			return b.take(4)
		}
		if v.Len() > b.nodes {
			return ErrSandboxOutputLimit
		}
		if err := b.take(2 + max(0, v.Len()-1)); err != nil {
			return err
		}
		iter := v.MapRange()
		for iter.Next() {
			if err := b.text(iter.Key().String()); err != nil {
				return err
			}
			if err := b.take(1); err != nil {
				return err
			}
			if err := b.input(iter.Value(), depth+1); err != nil {
				return err
			}
		}
		return nil
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() {
			return b.take(4)
		}
		if v.Len() > b.nodes {
			return ErrSandboxOutputLimit
		}
		if err := b.take(2 + max(0, v.Len()-1)); err != nil {
			return err
		}
		for i := 0; i < v.Len(); i++ {
			if err := b.input(v.Index(i), depth+1); err != nil {
				return err
			}
		}
		return nil
	default:
		return ErrRuleExecution
	}
}

func exportBounded(ctx context.Context, value goja.Value, limit int) (any, error) {
	b := &valueBudget{ctx: ctx, bytes: limit, nodes: DefaultValueNodes}
	return b.output(value, 0, map[*goja.Object]bool{})
}
func (b *valueBudget) output(value goja.Value, depth int, active map[*goja.Object]bool) (any, error) {
	if err := b.node(depth); err != nil {
		return nil, err
	}
	if value == nil || goja.IsNull(value) || goja.IsUndefined(value) {
		return nil, b.take(4)
	}
	if _, ok := value.(*goja.Symbol); ok {
		return nil, ErrRuleExecution
	}
	if obj, ok := value.(*goja.Object); ok {
		if active[obj] {
			return nil, ErrRuleExecution
		}
		active[obj] = true
		defer delete(active, obj)
		if _, callable := goja.AssertFunction(obj); callable {
			return nil, ErrRuleExecution
		}
		switch obj.ClassName() {
		case "Array":
			n := obj.Get("length").ToInteger()
			if n < 0 || n > int64(b.nodes) {
				return nil, ErrSandboxOutputLimit
			}
			if err := b.take(2 + max(0, int(n)-1)); err != nil {
				return nil, err
			}
			out := make([]any, int(n))
			for i := range out {
				var err error
				out[i], err = b.output(obj.Get(strconv.Itoa(i)), depth+1, active)
				if err != nil {
					return nil, err
				}
			}
			return out, nil
		case "Object":
			keys := obj.Keys()
			if len(keys) > b.nodes {
				return nil, ErrSandboxOutputLimit
			}
			if err := b.take(2 + max(0, len(keys)-1)); err != nil {
				return nil, err
			}
			out := make(map[string]any, len(keys))
			for _, key := range keys {
				if err := b.text(key); err != nil {
					return nil, err
				}
				if err := b.take(1); err != nil {
					return nil, err
				}
				v, err := b.output(obj.Get(key), depth+1, active)
				if err != nil {
					return nil, err
				}
				out[key] = v
			}
			return out, nil
		default:
			return nil, ErrRuleExecution
		}
	}
	v := value.Export()
	switch v.(type) {
	case string:
		return v, b.text(v.(string))
	case bool, int64, int32, int, float64, float32, nil:
		return v, b.scalar(v)
	default:
		return nil, ErrRuleExecution
	}
}
