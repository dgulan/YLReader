package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"path/filepath"
	"reflect"
	"runtime"
	"sync/atomic"
	"time"
)

type remoteExecutor struct {
	cfg       Config
	client    HTTPClient
	endpoints chan string
	paths     []string
	slots     chan struct{}
	inflight  *byteBudget
}

// NewRemote enables isolated workers explicitly. It never falls back to local JS.
func NewRemote(cfg Config, client HTTPClient, paths []string) (*Sandbox, error) {
	if runtime.GOOS != "linux" || len(paths) == 0 || len(paths) > 64 {
		return nil, ErrWorkerUnavailable
	}
	seen := map[string]bool{}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || seen[path] {
			return nil, ErrWorkerUnavailable
		}
		seen[path] = true
	}
	cfg = normalizeConfig(cfg)
	cfg.ScriptTimeout = min(cfg.ScriptTimeout, DefaultScriptTimeout)
	cfg.MaxConcurrent = min(cfg.MaxConcurrent, len(paths))
	s := New(cfg, client)
	r := &remoteExecutor{cfg: cfg, client: client, endpoints: make(chan string, len(paths)), paths: append([]string(nil), paths...), slots: s.slots, inflight: newByteBudget(cfg.InflightBytes)}
	for _, path := range paths {
		r.endpoints <- path
	}
	s.remote = r
	return s, nil
}

func dialWorker(ctx context.Context, path string) (net.Conn, error) {
	if checkIPCSocket(path) != nil {
		return nil, ErrWorkerUnavailable
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, ErrWorkerUnavailable
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	return conn, nil
}
func readWorkerHello(conn net.Conn) error {
	f, err := readWorkerFrame(conn, func(k byte, s uint32) bool { return k == wireHello && s == 0 })
	if err != nil {
		return err
	}
	var h workerHello
	if decodeWorkerJSON(f.Data, &h, "profile", "memory_max") != nil || h.Profile != "cgroup-v2-v1" || h.MemoryMax <= 0 || h.MemoryMax > 256<<20 {
		return ErrWorkerUnavailable
	}
	return nil
}

// Probe checks every configured endpoint without evaluating a rule or doing HTTP.
func (s *Sandbox) Probe(ctx context.Context) error {
	if s.remote == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for _, path := range s.remote.paths {
		conn, err := dialWorker(ctx, path)
		if err != nil {
			return ErrWorkerUnavailable
		}
		stop := context.AfterFunc(ctx, func() { conn.Close() })
		err = readWorkerHello(conn)
		conn.Close()
		stop()
		if err != nil {
			return ErrWorkerUnavailable
		}
	}
	return nil
}

func (r *remoteExecutor) evaluate(parent context.Context, script string, vars map[string]any, structured bool) (any, error) {
	deadline, cancelDeadline := context.WithTimeout(parent, r.cfg.ScriptTimeout)
	defer cancelDeadline()
	queue, cancelQueue := context.WithTimeout(deadline, r.cfg.QueueTimeout)
	defer cancelQueue()
	if queue.Err() != nil {
		return nil, ErrSandboxTimeout
	}
	select {
	case r.slots <- struct{}{}:
		defer func() { <-r.slots }()
	case <-queue.Done():
		return nil, ErrSandboxTimeout
	}
	if queue.Err() != nil {
		return nil, ErrSandboxTimeout
	}
	if len(script) > r.cfg.ScriptLimit {
		return nil, ErrRuleExecution
	}
	size, err := validateInput(deadline, vars, r.cfg.InputLimit)
	if err != nil {
		return nil, err
	}
	// Charge the encoded input before copying or marshaling it. Validation can
	// legitimately take longer than the queue budget on multi-megabyte inputs,
	// so budget admission gets its own QueueTimeout window from the script
	// deadline instead of inheriting an already-expired queue context. Total
	// admission wait stays bounded by two QueueTimeout windows.
	admit, cancelAdmit := context.WithTimeout(deadline, r.cfg.QueueTimeout)
	defer cancelAdmit()
	if err := r.inflight.acquire(admit, size); err != nil {
		return nil, ErrSandboxTimeout
	}
	defer r.inflight.release(size)
	cancelQueue()
	copyVars := map[string]any(nil)
	if vars != nil {
		copyVars = jsonInputCopy(reflect.ValueOf(vars)).(map[string]any)
	}
	if deadline.Err() != nil {
		return nil, ErrSandboxTimeout
	}
	path := <-r.endpoints
	defer func() { r.endpoints <- path }()
	fail := func() (any, error) {
		// Socket deadlines may fire before the context timer goroutine runs.
		until, timed := deadline.Deadline()
		if deadline.Err() != nil || (timed && !time.Now().Before(until)) {
			return nil, ErrSandboxTimeout
		}
		return nil, ErrRuleExecution
	}
	conn, err := dialWorker(deadline, path)
	if err != nil {
		return fail()
	}
	ctx, cancel := context.WithCancel(deadline)
	defer cancel()
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if readWorkerHello(conn) != nil {
		return fail()
	}
	if writeWorkerFrame(conn, wireEval, 0, workerEval{script, copyVars, structured}) != nil {
		return fail()
	}
	frames := make(chan workerFrame, 1)
	done := make(chan struct{})
	var expected atomic.Uint32
	expected.Store(1)
	var pending atomic.Bool
	go func() {
		defer close(done)
		defer close(frames)
		for {
			f, e := readWorkerFrame(conn, func(k byte, seq uint32) bool {
				return !pending.Load() && seq == expected.Load() && ((k == wireHTTP && seq <= workerHTTPMax) || k == wireResult)
			})
			if e != nil {
				cancel()
				return
			}
			if f.Kind == wireHTTP {
				pending.Store(true)
			} else {
				var extra [1]byte
				n, e := conn.Read(extra[:])
				if n != 0 || !errors.Is(e, io.EOF) {
					cancel()
					return
				}
			}
			select {
			case frames <- f:
			case <-ctx.Done():
				return
			}
			if f.Kind == wireResult {
				return
			}
		}
	}()
	defer func() { cancel(); conn.Close(); <-done }()
	for {
		var f workerFrame
		var ok bool
		select {
		case f, ok = <-frames:
			if !ok {
				return fail()
			}
		case <-ctx.Done():
			return fail()
		}
		if f.Kind == wireHTTP {
			var request workerHTTP
			if decodeWorkerJSON(f.Data, &request, "method", "url", "body") != nil || !validWorkerHTTP(request) {
				return fail()
			}
			reply := workerReply{Code: "rule_execution_failed"}
			if r.client != nil {
				body, e := r.client.Do(ctx, request.Method, request.URL, request.Body)
				if e == nil && len(body) <= 16<<20 {
					reply = workerReply{Body: body}
				} else if ctx.Err() != nil || errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) {
					reply.Code = "sandbox_timeout"
				}
			}
			if ctx.Err() != nil {
				return fail()
			}
			expected.Add(1)
			pending.Store(false)
			if writeWorkerFrame(conn, wireReply, f.Seq, reply) != nil {
				return fail()
			}
			continue
		}
		var result workerResult
		if decodeWorkerJSON(f.Data, &result, "ok") != nil {
			return fail()
		}
		if !result.OK {
			if len(result.Value) != 0 || result.Code == "" {
				return fail()
			}
			e := workerCodeError(result.Code)
			if e == errWorkerProtocol {
				return fail()
			}
			return nil, e
		}
		if result.Code != "" || len(result.Value) == 0 {
			return fail()
		}
		var value any
		d := json.NewDecoder(bytes.NewReader(result.Value))
		d.UseNumber()
		if d.Decode(&value) != nil {
			return fail()
		}
		value, err = wireNumbers(value)
		if err != nil {
			return fail()
		}
		if structured {
			if err := validateWorkerValue(ctx, value, r.cfg.OutputLimit); err != nil {
				return nil, err
			}
			return value, nil
		}
		text, ok := value.(string)
		if !ok {
			return fail()
		}
		if len(text) > r.cfg.OutputLimit {
			return nil, ErrSandboxOutputLimit
		}
		return text, nil
	}
}
func validWorkerHTTP(r workerHTTP) bool {
	return (r.Method == "GET" || r.Method == "POST") && len(r.URL) > 0 && len(r.URL) <= 16<<10 && len(r.Body) <= 1<<20 && (r.Method != "GET" || len(r.Body) == 0)
}
func validateWorkerValue(ctx context.Context, v any, limit int) error {
	b := valueBudget{ctx: ctx, bytes: limit, nodes: DefaultValueNodes}
	return b.input(reflect.ValueOf(v), 0)
}
