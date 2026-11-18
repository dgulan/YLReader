package sandbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

func TestResourceRuntimeIsolation(t *testing.T) {
	s := New(Config{}, nil)
	_, err := s.Eval(context.Background(), `var leaked="private"; let lexical="private"; Object.prototype.pwn="private"; java.md5Encode=function(){return "bad"}; "ok"`, nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		out, err := s.Eval(context.Background(), `[typeof leaked,typeof lexical,({}).pwn,java.md5Encode("a")].join("|")`, nil)
		if err != nil || out != "undefined|undefined||0cc175b9c0f1b6a831c399e269772661" {
			t.Fatal("cross-call state leak", out, err)
		}
	}
	out, err := s.Eval(context.Background(), `typeof java.ajax+","+typeof source.setVariable+","+book.name`, map[string]any{"java": "override", "source": nil, "book": false, "bookName": "title"})
	if err != nil || out != "function,function,title" {
		t.Fatal("bridge overwritten by input", out, err)
	}
}

func TestResourceConversionDeadline(t *testing.T) {
	for _, tc := range []struct {
		script     string
		structured bool
	}{
		{`({toString(){while(true){}}})`, false},
		{`({get x(){while(true){}}})`, true},
		{`Object.defineProperty(globalThis,"result",{get(){while(true){}}}); undefined`, false},
	} {
		s := New(Config{ScriptTimeout: 40 * time.Millisecond}, nil)
		var err error
		if tc.structured {
			_, err = s.EvalValue(context.Background(), tc.script, nil)
		} else {
			_, err = s.Eval(context.Background(), tc.script, nil)
		}
		if !errors.Is(err, ErrSandboxTimeout) {
			t.Fatal("conversion not interrupted", err)
		}
		if out, err := s.Eval(context.Background(), `"ok"`, nil); err != nil || out != "ok" {
			t.Fatal("late interrupt or slot leak", out, err)
		}
	}
}

func TestResourceInputBeforeCompilation(t *testing.T) {
	s := New(Config{ScriptLimit: 16, InputLimit: 64}, nil)
	_, err := s.Eval(context.Background(), strings.Repeat("x", 1000), nil)
	if !errors.Is(err, ErrRuleExecution) || len(s.programs.m) != 0 {
		t.Fatal("oversized code compiled", err)
	}
	called := false
	for _, vars := range []map[string]any{{"x": strings.Repeat("x", 1000)}, {"callback": func() { called = true }}} {
		if _, err := s.Eval(context.Background(), "callback()", vars); !errors.Is(err, ErrRuleExecution) {
			t.Fatal(err)
		}
	}
	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, err := s.Eval(context.Background(), "1", cycle); !errors.Is(err, ErrRuleExecution) {
		t.Fatal("cyclic input not bounded", err)
	}
	if called || len(s.programs.m) != 0 {
		t.Fatal("untrusted Go callback or compile was invoked")
	}
}

func TestResourceStructuredExport(t *testing.T) {
	s := New(Config{OutputLimit: 64}, nil)
	value, err := s.EvalValue(context.Background(), `({name:"中文",items:[1,true,null]})`, nil)
	if err != nil || value.(map[string]any)["name"] != "中文" {
		t.Fatal(value, err)
	}
	for _, tc := range []struct {
		script string
		want   error
	}{
		{`new Array(100000)`, ErrSandboxOutputLimit},
		{`({x:"a".repeat(100)})`, ErrSandboxOutputLimit},
		{`var a={};a.self=a;a`, ErrRuleExecution},
		{`(function(){})`, ErrRuleExecution},
		{`var a={};var t=a;for(var i=0;i<40;i++){t.x={};t=t.x;}a`, ErrSandboxOutputLimit},
	} {
		if _, err := s.EvalValue(context.Background(), tc.script, nil); !errors.Is(err, tc.want) {
			t.Fatal(tc.script, err)
		}
	}
}

type blockingResourceClient struct{ entered, release chan struct{} }

func (c blockingResourceClient) Do(context.Context, string, string, []byte) ([]byte, error) {
	close(c.entered)
	<-c.release
	return []byte("ok"), nil
}

func TestResourceAdmissionHoldsNativeWorkUntilItStops(t *testing.T) {
	client := blockingResourceClient{make(chan struct{}), make(chan struct{})}
	s := New(Config{MaxConcurrent: 1, QueueTimeout: 10 * time.Millisecond}, client)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := s.Eval(ctx, `java.ajax("https://synthetic.invalid")`, nil); done <- err }()
	select {
	case <-client.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("bridge never entered")
	}
	cancel()
	_, secondErr := s.Eval(context.Background(), `"second"`, nil)
	if !errors.Is(secondErr, ErrSandboxTimeout) {
		close(client.release)
		t.Fatal("native slot released early", secondErr)
	}
	select {
	case <-done:
		close(client.release)
		t.Fatal("abandoned native call")
	default:
	}
	close(client.release)
	select {
	case err := <-done:
		if !errors.Is(err, ErrSandboxTimeout) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("native completion stuck")
	}
	if out, err := s.Eval(context.Background(), `"third"`, nil); err != nil || out != "third" {
		t.Fatal(out, err)
	}
}

func TestResourceCompileDoesNotBlockCacheHit(t *testing.T) {
	c := newProgramCache(8, 1024)
	program, err := goja.Compile("rule", "1", false)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.get("cached", func() (*goja.Program, error) { return program, nil })
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_, _ = c.get("slow", func() (*goja.Program, error) { close(started); <-release; return program, nil })
	}()
	<-started
	hit := make(chan struct{})
	go func() {
		defer close(hit)
		_, _ = c.get("cached", func() (*goja.Program, error) { t.Error("cache hit recompiled"); return program, nil })
	}()
	select {
	case <-hit:
	case <-time.After(time.Second):
		close(release)
		<-done
		t.Fatal("compiler held cache lock")
	}
	close(release)
	<-done
}

func BenchmarkResourceFreshEval(b *testing.B) {
	s := New(Config{}, nil)
	vars := map[string]any{"result": "正文"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.Eval(context.Background(), `result + "!"`, vars); err != nil {
			b.Fatal(err)
		}
	}
}

type panicResourceClient struct{}

func (panicResourceClient) Do(context.Context, string, string, []byte) ([]byte, error) {
	panic("private native detail")
}
func TestResourcePanicAndCanceledAdmission(t *testing.T) {
	s := New(Config{}, panicResourceClient{})
	_, err := s.Eval(context.Background(), `java.ajax("https://synthetic.invalid")`, nil)
	if !errors.Is(err, ErrSandboxPanic) || strings.Contains(err.Error(), "private native") {
		t.Fatal("panic not contained", err)
	}
	if out, err := s.Eval(context.Background(), `"ok"`, nil); err != nil || out != "ok" {
		t.Fatal("panic leaked slot", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.Eval(ctx, `while(true){}`, nil)
	if !errors.Is(err, ErrSandboxTimeout) || len(s.slots) != 0 {
		t.Fatal("canceled request admitted", err)
	}
}
func BenchmarkResourceStructuredExport(b *testing.B) {
	s := New(Config{}, nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := s.EvalValue(context.Background(), `({name:"book",items:[1,2,3,true]})`, nil); err != nil {
			b.Fatal(err)
		}
	}
}
