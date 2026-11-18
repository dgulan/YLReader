// Package sandbox 提供 goja JS 规则沙箱：双层超时、输出上限、白名单桥接、
// 每次独立 Runtime 与有界 Program 编译缓存。
package sandbox

import (
	"context"
	"errors"
	"time"

	"github.com/dop251/goja"
)

// 哨兵错误，严格区分超时 / 输出超限 / panic / 规则执行失败 / 不支持的规则。
var (
	// ErrSandboxTimeout 表示执行超时（外层 reqCtx 或内层 ScriptTimeout 触发）。
	ErrSandboxTimeout = errors.New("sandbox: execution timed out")
	// ErrSandboxOutputLimit 表示结果字符串超过 OutputLimit。
	ErrSandboxOutputLimit = errors.New("sandbox: output exceeds limit")
	// ErrSandboxPanic 表示桥接函数或运行时触发未预期的 Go panic。
	ErrSandboxPanic = errors.New("sandbox: runtime panic")
	// ErrRuleExecution 表示 JS 规则执行失败（脚本异常、桥接调用失败等）。
	ErrRuleExecution = errors.New("sandbox: rule execution failed")
	// ErrUnsupportedRule 表示规则类型不受支持。
	ErrUnsupportedRule = errors.New("sandbox: unsupported rule")
)

// 默认参数。
const (
	DefaultScriptTimeout       = 3 * time.Second
	DefaultOutputLimit         = 1 << 20 // 1,048,576 字节
	DefaultProgramCacheEntries = 2048
	DefaultProgramCacheBytes   = 32 << 20 // 32 MiB
	DefaultScriptLimit         = 256 << 10
	DefaultInputLimit          = 32 << 20
	DefaultMaxConcurrent       = 4
	DefaultQueueTimeout        = 250 * time.Millisecond
	DefaultValueDepth          = 32
	DefaultValueNodes          = 65536
)

// HTTPClient 是沙箱桥接函数（java.ajax/get/post）使用的出网通道。实现方必须
// 保证：ctx 取消时立即中断阻塞 I/O（内部使用 http.NewRequestWithContext）。
type HTTPClient interface {
	Do(ctx context.Context, method, url string, body []byte) ([]byte, error)
}

// Config 定义沙箱参数。零值字段取默认值。
type Config struct {
	ScriptTimeout       time.Duration
	OutputLimit         int
	ProgramCacheEntries int
	ProgramCacheBytes   int64
	ScriptLimit         int
	InputLimit          int
	InflightBytes       int64
	MaxConcurrent       int
	QueueTimeout        time.Duration
}

// execCtx 是单个执行周期的可变上下文，桥接函数通过它访问当前 ctx 与 HTTP 客户端。
// Runtime 被独占借用，因此无并发写入。
type execCtx struct {
	ctx        context.Context
	client     HTTPClient
	vars       map[string]any
	lastErr    error
	sourceVars map[string]any
}

// runtimeState 封装一个 goja.Runtime 及其共享的 execCtx 指针。
type runtimeState struct {
	rt *goja.Runtime
	ec *execCtx
}

// Sandbox 是线程安全的 JS 规则执行沙箱。
type Sandbox struct {
	cfg      Config
	client   HTTPClient
	slots    chan struct{}
	programs *programCache
	remote   *remoteExecutor
}

// New 构建沙箱。client 为 nil 时 java.* 网络桥接会抛出规则错误。
func New(cfg Config, client HTTPClient) *Sandbox {
	cfg = normalizeConfig(cfg)
	s := &Sandbox{
		cfg:      cfg,
		client:   client,
		programs: newProgramCache(cfg.ProgramCacheEntries, cfg.ProgramCacheBytes),
	}
	s.slots = make(chan struct{}, cfg.MaxConcurrent)
	return s
}

func normalizeConfig(c Config) Config {
	if c.ScriptTimeout <= 0 {
		c.ScriptTimeout = DefaultScriptTimeout
	}
	if c.OutputLimit <= 0 {
		c.OutputLimit = DefaultOutputLimit
	}
	if c.ProgramCacheEntries <= 0 {
		c.ProgramCacheEntries = DefaultProgramCacheEntries
	}
	if c.ProgramCacheBytes <= 0 {
		c.ProgramCacheBytes = DefaultProgramCacheBytes
	}
	if c.ScriptLimit <= 0 {
		c.ScriptLimit = DefaultScriptLimit
	}
	if c.InputLimit <= 0 {
		c.InputLimit = DefaultInputLimit
	}
	// One maximum-size request must always fit; a smaller explicit budget
	// would otherwise reject every large input at admission time.
	if minimum := int64(c.InputLimit) * inflightCostFactor; c.InflightBytes < minimum {
		c.InflightBytes = minimum
	}
	if c.MaxConcurrent <= 0 {
		c.MaxConcurrent = DefaultMaxConcurrent
	}
	if c.MaxConcurrent > 64 {
		c.MaxConcurrent = 64
	}
	if c.QueueTimeout <= 0 {
		c.QueueTimeout = DefaultQueueTimeout
	}
	return c
}

// Eval executes and converts the result under one deadline and one admission slot.
func (s *Sandbox) Eval(ctx context.Context, script string, vars map[string]any) (string, error) {
	value, err := s.evaluate(ctx, script, vars, false)
	if err != nil {
		return "", err
	}
	return value.(string), nil
}

func (s *Sandbox) EvalValue(ctx context.Context, script string, vars map[string]any) (any, error) {
	return s.evaluate(ctx, script, vars, true)
}

func (s *Sandbox) evaluate(parent context.Context, script string, vars map[string]any, structured bool) (result any, err error) {
	if s.remote != nil {
		return s.remote.evaluate(parent, script, vars, structured)
	}
	ctx, cancel := context.WithTimeout(parent, s.cfg.ScriptTimeout)
	defer cancel()
	queue, stopQueue := context.WithTimeout(ctx, s.cfg.QueueTimeout)
	defer stopQueue()
	if queue.Err() != nil {
		return nil, ErrSandboxTimeout
	}
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-queue.Done():
		return nil, ErrSandboxTimeout
	}
	if queue.Err() != nil {
		return nil, ErrSandboxTimeout
	}
	stopQueue()
	if ctx.Err() != nil {
		return nil, ErrSandboxTimeout
	}
	if len(script) > s.cfg.ScriptLimit {
		return nil, ErrRuleExecution
	}
	if _, err := validateInput(ctx, vars, s.cfg.InputLimit); err != nil {
		return nil, err
	}
	// Compilation remains synchronous: never abandon native work and release its slot.
	defer func() {
		if recover() != nil {
			result = nil
			err = ErrSandboxPanic
		}
	}()
	program, err := s.programs.get(script, func() (*goja.Program, error) { return goja.Compile("rule", script, false) })
	if ctx.Err() != nil {
		return nil, ErrSandboxTimeout
	}
	if err != nil {
		return nil, ErrRuleExecution
	}
	state := s.newRuntime()
	state.ec.ctx, state.ec.vars, state.ec.sourceVars = ctx, vars, map[string]any{}
	done, stopped := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			state.rt.Interrupt(ErrSandboxTimeout)
		case <-done:
		}
	}()
	// Keep the watcher through injection, result fallback, getters and conversion.
	func() {
		defer func() {
			if r := recover(); r != nil {
				switch r.(type) {
				case *goja.InterruptedError:
					err = ErrSandboxTimeout
				case *goja.Exception, *goja.StackOverflowError:
					err = ErrRuleExecution
				default:
					err = ErrSandboxPanic
				}
			}
		}()
		exception := state.rt.Try(func() {
			setVars(state.rt, vars)
			var value goja.Value
			value, err = state.rt.RunProgram(program)
			if err != nil {
				return
			}
			if goja.IsUndefined(value) || goja.IsNull(value) {
				value = state.rt.Get("result")
			}
			if value == nil {
				value = goja.Undefined()
			}
			if structured {
				result, err = exportBounded(ctx, value, s.cfg.OutputLimit)
			} else {
				out := value.String()
				if len(out) > s.cfg.OutputLimit {
					err = ErrSandboxOutputLimit
					return
				}
				result = out
			}
		})
		if exception != nil {
			err = ErrRuleExecution
		}
	}()
	close(done)
	<-stopped
	if ctx.Err() != nil {
		return nil, ErrSandboxTimeout
	}
	var interrupted *goja.InterruptedError
	if errors.As(err, &interrupted) {
		return nil, ErrSandboxTimeout
	}
	if state.ec.lastErr != nil {
		return nil, state.ec.lastErr
	}
	if err != nil {
		if errors.Is(err, ErrSandboxTimeout) || errors.Is(err, ErrSandboxPanic) || errors.Is(err, ErrSandboxOutputLimit) || errors.Is(err, ErrRuleExecution) {
			return nil, err
		}
		return nil, ErrRuleExecution
	}
	return result, nil
}

func (s *Sandbox) newRuntime() *runtimeState {
	ec := &execCtx{client: s.client}
	rt := goja.New()
	rt.SetMaxCallStackSize(1024)
	installBridge(rt, ec)
	return &runtimeState{rt: rt, ec: ec}
}

func setVars(rt *goja.Runtime, vars map[string]any) {
	for k, v := range vars {
		if k == "java" || k == "source" || k == "book" {
			continue
		}
		rt.Set(k, v)
	}
	if _, ok := vars["result"]; !ok {
		rt.Set("result", goja.Undefined())
	}
	book := rt.Get("book").ToObject(rt)
	book.Set("name", vars["bookName"])
	book.Set("author", vars["bookAuthor"])
	book.Set("bookUrl", vars["bookUrl"])
}
