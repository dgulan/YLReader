package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync/atomic"
	"time"
)

type workerProxy struct {
	ctx     context.Context
	conn    net.Conn
	replies chan workerFrame
	pending atomic.Uint32
	seq     uint32
}

func (p *workerProxy) Do(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	request := workerHTTP{method, url, body}
	if !validWorkerHTTP(request) || p.seq >= workerHTTPMax {
		return nil, ErrRuleExecution
	}
	p.seq++
	p.pending.Store(p.seq)
	if err := writeWorkerFrame(p.conn, wireHTTP, p.seq, request); err != nil {
		return nil, ErrRuleExecution
	}
	select {
	case <-ctx.Done():
		return nil, context.DeadlineExceeded
	case f, ok := <-p.replies:
		p.pending.Store(0)
		if !ok || f.Seq != p.seq {
			return nil, ErrRuleExecution
		}
		var reply workerReply
		if decodeWorkerJSON(f.Data, &reply, "body", "code") != nil || len(reply.Body) > 16<<20 {
			return nil, ErrRuleExecution
		}
		if reply.Code != "" {
			if len(reply.Body) != 0 {
				return nil, ErrRuleExecution
			}
			e := workerCodeError(reply.Code)
			if errors.Is(e, ErrSandboxTimeout) {
				return nil, context.DeadlineExceeded
			}
			return nil, ErrRuleExecution
		}
		return reply.Body, nil
	}
}

// ServeWorker requires OS isolation and a terminator which exits this worker process.
// The API must never call this function in its own process.
func ServeWorker(ctx context.Context, path string, terminate func()) error {
	if terminate == nil {
		return ErrWorkerUnavailable
	}
	hello, err := workerIsolation()
	if err != nil {
		return ErrWorkerUnavailable
	}
	listener, cleanup, err := listenWorkerSocket(path)
	if err != nil {
		return ErrWorkerUnavailable
	}
	defer cleanup()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	programs := newProgramCache(DefaultProgramCacheEntries, DefaultProgramCacheBytes)
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return ErrWorkerUnavailable
		}
		serveWorkerSessionCached(ctx, conn, hello, terminate, programs)
	}
}

func serveWorkerSession(parent context.Context, conn net.Conn, hello workerHello, terminate func()) {
	serveWorkerSessionCached(parent, conn, hello, terminate, nil)
}

func serveWorkerSessionCached(parent context.Context, conn net.Conn, hello workerHello, terminate func(), programs *programCache) {
	defer conn.Close()
	ctx, cancel := context.WithTimeout(parent, DefaultScriptTimeout)
	defer cancel()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if writeWorkerFrame(conn, wireHello, 0, hello) != nil {
		return
	}
	f, err := readWorkerFrame(conn, func(k byte, s uint32) bool { return k == wireEval && s == 0 })
	if err != nil {
		return
	}
	var request workerEval
	if decodeWorkerJSON(f.Data, &request, "script", "vars", "structured") != nil {
		return
	}
	if request.Vars != nil {
		v, e := wireNumbers(request.Vars)
		if e != nil {
			return
		}
		request.Vars = v.(map[string]any)
	}
	proxy := &workerProxy{ctx: ctx, conn: conn, replies: make(chan workerFrame, 1)}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(proxy.replies)
		for seq := uint32(1); ; seq++ {
			f, err := readWorkerFrame(conn, func(k byte, s uint32) bool {
				return k == wireReply && s == seq && s == proxy.pending.Load() && s <= workerHTTPMax
			})
			if err != nil {
				cancel()
				return
			}
			select {
			case proxy.replies <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); conn.Close(); <-readerDone }()
	evalDone, watchDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(watchDone)
		watchWorker(ctx, evalDone, terminate)
	}()
	sb := New(Config{MaxConcurrent: 1}, proxy)
	if programs != nil {
		sb.programs = programs
	}
	var value any
	if request.Structured {
		value, err = sb.EvalValue(ctx, request.Script, request.Vars)
	} else {
		value, err = sb.Eval(ctx, request.Script, request.Vars)
	}
	close(evalDone)
	<-watchDone
	if ctx.Err() != nil {
		return
	} // Parent deadline/EOF already owns the failure; do not replay work.
	result := workerResult{OK: err == nil}
	if err != nil {
		result.Code = workerErrorCode(err)
	} else {
		result.Value, err = json.Marshal(value)
		if err != nil {
			result = workerResult{Code: "rule_execution_failed"}
		}
	}
	_ = writeWorkerFrame(conn, wireResult, proxy.seq+1, result)
}

func watchWorker(ctx context.Context, done <-chan struct{}, terminate func()) {
	select {
	case <-done:
		return
	case <-ctx.Done():
	}
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		terminate()
	}
}
