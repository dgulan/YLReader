//go:build linux

package sandbox

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type workerHTTPFunc func(context.Context, string, string, []byte) ([]byte, error)

func (f workerHTTPFunc) Do(ctx context.Context, m, u string, b []byte) ([]byte, error) {
	return f(ctx, m, u, b)
}

func workerTestEndpoint(t *testing.T, handler func(net.Conn)) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "worker.sock")
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	l, cleanup, err := listenWorkerSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			handler(c)
		}
	}()
	t.Cleanup(func() { cleanup(); <-done })
	return path
}
func fixtureWorker(conn net.Conn) {
	serveWorkerSession(context.Background(), conn, workerHello{"cgroup-v2-v1", 128 << 20}, func() { panic("fixture watchdog unexpectedly fired") })
}

func TestWorkerRemoteRoundTripAndProxyContext(t *testing.T) {
	path := workerTestEndpoint(t, fixtureWorker)
	type ownerKey struct{}
	ctx := context.WithValue(context.Background(), ownerKey{}, "parent-only")
	var calls atomic.Int32
	client := workerHTTPFunc(func(ctx context.Context, method, url string, body []byte) ([]byte, error) {
		if ctx.Value(ownerKey{}) != "parent-only" || method != "POST" || string(body) != "body" {
			t.Error("trusted context or proxy data lost")
		}
		calls.Add(1)
		return []byte("proxy-response"), nil
	})
	s, err := NewRemote(Config{}, client, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	out, err := s.Eval(ctx, `java.post("https://example.com","body")`, nil)
	if err != nil || out != "proxy-response" || calls.Load() != 1 {
		t.Fatal(out, err, calls.Load())
	}
	vars := map[string]any{"object": map[string]any{"x": 1}}
	value, err := s.EvalValue(ctx, `object.x=2;({value:object.x,items:[1,true,null]})`, vars)
	if err != nil || value.(map[string]any)["value"] != int64(2) || vars["object"].(map[string]any)["x"] != 1 {
		t.Fatal(value, vars, err)
	}
	_, err = s.Eval(ctx, `var leak="private";Object.prototype.pwn="private";"ok"`, nil)
	if err != nil {
		t.Fatal(err)
	}
	out, err = s.Eval(ctx, `typeof leak+","+typeof ({}).pwn`, nil)
	if err != nil || out != "undefined,undefined" {
		t.Fatal(out, err)
	}
}

func TestWorkerEOFAbortsHTTPWithoutReplay(t *testing.T) {
	entered, stopped := make(chan struct{}), make(chan struct{})
	var accepts atomic.Int32
	path := workerTestEndpoint(t, func(conn net.Conn) {
		if accepts.Add(1) > 1 {
			fixtureWorker(conn)
			return
		}
		defer conn.Close()
		_ = writeWorkerFrame(conn, wireHello, 0, workerHello{"cgroup-v2-v1", 128 << 20})
		if _, err := readWorkerFrame(conn, func(k byte, s uint32) bool { return k == wireEval && s == 0 }); err != nil {
			return
		}
		_ = writeWorkerFrame(conn, wireHTTP, 1, workerHTTP{Method: "POST", URL: "https://example.com", Body: []byte("once")})
		<-entered
	})
	var calls atomic.Int32
	client := workerHTTPFunc(func(ctx context.Context, _ string, _ string, _ []byte) ([]byte, error) {
		calls.Add(1)
		close(entered)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	})
	s, err := NewRemote(Config{}, client, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Eval(context.Background(), `"ignored"`, nil)
	if !errors.Is(err, ErrRuleExecution) || calls.Load() != 1 || accepts.Load() != 1 {
		t.Fatal("crash replayed", err, calls.Load(), accepts.Load())
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("HTTP not canceled")
	}
	if out, err := s.Eval(context.Background(), `"recovered"`, nil); err != nil || out != "recovered" {
		t.Fatal("pool did not recover", out, err)
	}
}

func TestWorkerProxyBudgetAndCancellation(t *testing.T) {
	path := workerTestEndpoint(t, fixtureWorker)
	var calls atomic.Int32
	client := workerHTTPFunc(func(context.Context, string, string, []byte) ([]byte, error) { calls.Add(1); return []byte("ok"), nil })
	s, err := NewRemote(Config{}, client, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Eval(context.Background(), `for(var i=0;i<65;i++){java.ajax("https://example.com")}`, nil)
	if !errors.Is(err, ErrRuleExecution) || calls.Load() != 64 {
		t.Fatal("HTTP budget not enforced", err, calls.Load())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	_, err = s.Eval(ctx, `({toString(){while(true){}}})`, nil)
	if !errors.Is(err, ErrSandboxTimeout) {
		t.Fatal(err)
	}
	if out, err := s.Eval(context.Background(), `"ok"`, nil); err != nil || out != "ok" {
		t.Fatal(out, err)
	}
}

func TestWorkerSocketOwnershipAndLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "worker.sock")
	if err := os.Chmod(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	l, cleanup, err := listenWorkerSocket(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := listenWorkerSocket(path); err == nil {
		cleanup()
		t.Fatal("active socket replaced")
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	cleanup()
	_, cleanup, err = listenWorkerSocket(path)
	if err != nil {
		t.Fatal("stale socket not recovered", err)
	}
	cleanup()
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := listenWorkerSocket(path); err == nil {
		t.Fatal("ordinary file replaced")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "keep" {
		t.Fatal("file was removed")
	}
}

func TestWorkerWatchdogHelper(t *testing.T) {
	if os.Getenv("YLREADER_WORKER_WATCHDOG_HELPER") != "1" {
		t.Skip("subprocess-only helper")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	watchWorker(ctx, make(chan struct{}), func() { os.Exit(124) })
	os.Exit(2)
}
func TestWorkerWatchdogTerminatesProcess(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkerWatchdogHelper$")
	cmd.Env = []string{"YLREADER_WORKER_WATCHDOG_HELPER=1"}
	err := cmd.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 124 {
		t.Fatal("watchdog did not terminate helper", err)
	}
}

func TestWorkerUnavailableDoesNotEvaluateLocally(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent.sock")
	s, err := NewRemote(Config{}, nil, []string{path})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Eval(context.Background(), `"would succeed locally"`, nil); !errors.Is(err, ErrRuleExecution) {
		t.Fatal(err)
	}
	if err := s.Probe(context.Background()); !errors.Is(err, ErrWorkerUnavailable) {
		t.Fatal(err)
	}
	if _, err := NewRemote(Config{}, nil, []string{path, path}); err == nil {
		t.Fatal("duplicate endpoint")
	}
}
