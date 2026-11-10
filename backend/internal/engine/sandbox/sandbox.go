// Package sandbox 提供 goja JS 规则沙箱：双层超时、输出上限、白名单桥接、
// Runtime 池化与 Program 编译缓存。超时/panic/超限会丢弃 Runtime 不回池。
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
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
	pool     sync.Pool
	programs *programCache
}

// New 构建沙箱。client 为 nil 时 java.* 网络桥接会抛出规则错误。
func New(cfg Config, client HTTPClient) *Sandbox {
	cfg = normalizeConfig(cfg)
	s := &Sandbox{
		cfg:      cfg,
		client:   client,
		programs: newProgramCache(cfg.ProgramCacheEntries, cfg.ProgramCacheBytes),
	}
	s.pool = sync.Pool{New: func() any { return s.newRuntime() }}
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
	return c
}

// Eval 执行一段 JS，返回表达式的完成值（与 Legado evalJS 一致）；若完成值为
// undefined/null（例如脚本仅含 var 声明），回退到 result 全局变量。
// 外层 ctx 为调用方注入的总超时；内层 ScriptTimeout 叠加在 ctx 之上。
func (s *Sandbox) Eval(ctx context.Context, script string, vars map[string]any) (string, error) {
	value, state, tempKeys, err := s.evalRaw(ctx, script, vars)
	if err != nil {
		return "", err
	}
	out := value.String()
	if len(out) > s.cfg.OutputLimit {
		// 丢弃 Runtime（不回池），避免超大输出污染后续调用。
		return "", ErrSandboxOutputLimit
	}
	s.cleanup(state, tempKeys)
	s.pool.Put(state)
	return out, nil
}

// EvalValue 执行一段 JS 并返回导出后的 Go 值（数组→[]any，对象→map[string]any，
// 标量→对应 Go 类型）；undefined/null 返回 nil。用于列表切分等需要结构化结果的场景。
func (s *Sandbox) EvalValue(ctx context.Context, script string, vars map[string]any) (any, error) {
	value, state, tempKeys, err := s.evalRaw(ctx, script, vars)
	if err != nil {
		return nil, err
	}
	var out any
	if !goja.IsUndefined(value) && !goja.IsNull(value) {
		out = value.Export()
	}
	s.cleanup(state, tempKeys)
	s.pool.Put(state)
	return out, nil
}

// evalRaw 执行脚本并返回完成值（含 result 全局兜底）。成功时调用方负责
// cleanup 与回池；失败时 Runtime 已按原逻辑处理。
func (s *Sandbox) evalRaw(ctx context.Context, script string, vars map[string]any) (goja.Value, *runtimeState, []string, error) {
	innerCtx, cancel := context.WithTimeout(ctx, s.cfg.ScriptTimeout)
	defer cancel()

	program, err := s.programs.get(script, func() (*goja.Program, error) {
		return goja.Compile("rule", script, false)
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w: %v", ErrRuleExecution, err)
	}

	state := s.acquire()
	ec := state.ec
	ec.ctx = innerCtx
	ec.vars = vars
	ec.lastErr = nil
	ec.sourceVars = map[string]any{}

	tempKeys := setVars(state.rt, vars)
	if _, ok := vars["result"]; !ok {
		state.rt.Set("result", goja.Undefined())
		tempKeys = append(tempKeys, "result")
	}

	value, err := s.run(state, innerCtx, program)
	if err != nil {
		if isDiscardErr(err) {
			return nil, nil, nil, err // 丢弃 Runtime
		}
		s.cleanup(state, tempKeys)
		s.pool.Put(state)
		return nil, nil, nil, err
	}

	resultVal := value
	if goja.IsUndefined(resultVal) || goja.IsNull(resultVal) {
		resultVal = state.rt.Get("result")
	}
	return resultVal, state, tempKeys, nil
}

func (s *Sandbox) acquire() *runtimeState {
	if v := s.pool.Get(); v != nil {
		return v.(*runtimeState)
	}
	return s.newRuntime()
}

func (s *Sandbox) newRuntime() *runtimeState {
	ec := &execCtx{client: s.client}
	rt := goja.New()
	// 栈深限制（对齐 architecture.md §3.3.1）：越界抛 RangeError，归为 ErrRuleExecution，
	// 不会被误判为超时 sentinel，也不会使进程崩溃。
	rt.SetMaxCallStackSize(1024)
	installBridge(rt, ec)
	return &runtimeState{rt: rt, ec: ec}
}

// run 执行程序并监视超时。innerCtx 到期时调用 rt.Interrupt 中断 CPU 密集 JS；
// 桥接函数的阻塞 I/O 则由 ctx 透传取消。
func (s *Sandbox) run(state *runtimeState, ctx context.Context, program *goja.Program) (goja.Value, error) {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			state.rt.Interrupt(ErrSandboxTimeout)
		case <-done:
		}
	}()

	var (
		value goja.Value
		err   error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("%w: %v", ErrSandboxPanic, r)
			}
		}()
		value, err = state.rt.RunProgram(program)
	}()

	close(done)
	<-stopped // 等待监视协程完全退出，避免残留 Interrupt 标志

	if err != nil {
		var ie *goja.InterruptedError
		if errors.As(err, &ie) {
			return nil, ErrSandboxTimeout
		}
		if state.ec.lastErr != nil {
			return nil, state.ec.lastErr
		}
		return nil, fmt.Errorf("%w: %v", ErrRuleExecution, err)
	}
	return value, nil
}

// cleanup 清理本周期注入的全局键并复位执行上下文，供回池复用。
func (s *Sandbox) cleanup(state *runtimeState, tempKeys []string) {
	state.rt.ClearInterrupt()
	for _, k := range tempKeys {
		state.rt.Set(k, goja.Undefined())
	}
	book := state.rt.Get("book").ToObject(state.rt)
	if book != nil {
		book.Set("name", goja.Undefined())
		book.Set("author", goja.Undefined())
		book.Set("bookUrl", goja.Undefined())
	}
	state.ec.ctx = nil
	state.ec.vars = nil
	state.ec.lastErr = nil
	state.ec.sourceVars = map[string]any{}
}

func isDiscardErr(err error) bool {
	return errors.Is(err, ErrSandboxTimeout) ||
		errors.Is(err, ErrSandboxOutputLimit) ||
		errors.Is(err, ErrSandboxPanic)
}

// setVars 注入用户变量与 book 只读字段，返回需清理的临时键。
func setVars(rt *goja.Runtime, vars map[string]any) []string {
	keys := make([]string, 0, len(vars)+1)
	for k, v := range vars {
		rt.Set(k, v)
		keys = append(keys, k)
	}
	book := rt.Get("book").ToObject(rt)
	if book != nil {
		book.Set("name", vars["bookName"])
		book.Set("author", vars["bookAuthor"])
		book.Set("bookUrl", vars["bookUrl"])
	}
	return keys
}
