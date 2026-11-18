//go:build integration && linux

package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
)

type performanceHTTP struct{}

func (performanceHTTP) Do(ctx context.Context, _, _ string, _ []byte) ([]byte, error) {
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return []byte("ok"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// slowHTTP holds an admitted evaluation open longer than QueueTimeout so
// concurrent callers exercise the refusal path instead of cascading through
// short sequential admissions.
type slowHTTP struct{ delay time.Duration }

func (h slowHTTP) Do(ctx context.Context, _, _ string, _ []byte) ([]byte, error) {
	timer := time.NewTimer(h.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return []byte("ok"), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func performancePercentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	sort.Float64s(v)
	return v[max(0, int(math.Ceil(float64(len(v))*p))-1)]
}

// parentMemoryPeak reports the cgroup memory high-water mark of this container
// so admission pressure can be compared with the parent memory limit.
func parentMemoryPeak() int64 {
	raw, err := os.ReadFile("/sys/fs/cgroup/memory.peak")
	if err != nil {
		return 0
	}
	n, _ := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	return n
}

func TestWorkerPerformance(t *testing.T) {
	mode := os.Getenv("TEST_WORKER_PERFORMANCE")
	if mode == "" {
		t.Skip("explicit isolated performance run only")
	}
	var sb *Sandbox
	var err error
	if mode == "local" {
		sb = New(Config{}, performanceHTTP{})
	} else if mode == "remote" {
		sb, err = NewRemote(Config{}, performanceHTTP{}, strings.Split(os.Getenv("TEST_WORKER_SOCKETS"), ","))
		if err != nil {
			t.Fatal(err)
		}
		if err = sb.Probe(context.Background()); err != nil {
			t.Fatal(err)
		}
	} else {
		t.Fatal("invalid performance mode")
	}
	type workload struct {
		name, script, data, expected string
		structured                   bool
	}
	workloads := []workload{
		{"short", `"ok"`, "", "ok", false},
		{"input-256KiB", `data.length.toString()`, strings.Repeat("a", 256<<10), strconv.Itoa(256 << 10), false},
		{"input-1MiB", `data.length.toString()`, strings.Repeat("a", 1<<20), strconv.Itoa(1 << 20), false},
		{"output-256KiB", `data`, strings.Repeat("b", 256<<10), strings.Repeat("b", 256<<10), false},
		{"structured-256", `Array.from({length:256},(_,i)=>({index:i,title:"chapter"}))`, "", "", true},
		{"http-20ms", `java.get("https://synthetic.invalid/")`, "", "ok", false},
	}
	if os.Getenv("TEST_WORKER_EDGES") == "1" || os.Getenv("TEST_WORKER_ADMISSION") == "1" {
		workloads = nil
	}
	for _, w := range workloads {
		for _, concurrent := range []int{1, 4, 16} {
			invoke := func() error {
				vars := map[string]any{"data": w.data}
				if w.structured {
					v, e := sb.EvalValue(context.Background(), w.script, vars)
					if e != nil {
						return e
					}
					a, ok := v.([]any)
					if !ok || len(a) != 256 {
						return fmt.Errorf("incorrect structured result")
					}
					return nil
				}
				v, e := sb.Eval(context.Background(), w.script, vars)
				if e == nil && v != w.expected {
					return fmt.Errorf("incorrect result")
				}
				return e
			}
			for i := 0; i < 8; i++ {
				if e := invoke(); e != nil {
					t.Fatalf("warmup %s: %v", w.name, e)
				}
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			all, good := []float64{}, []float64{}
			failures := map[string]int{}
			var mu sync.Mutex
			var next atomic.Int32
			start := time.Now()
			var wg sync.WaitGroup
			for j := 0; j < concurrent; j++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for next.Add(1) <= 100 {
						s := time.Now()
						e := invoke()
						ms := float64(time.Since(s).Nanoseconds()) / 1e6
						mu.Lock()
						all = append(all, ms)
						if e == nil {
							good = append(good, ms)
						} else {
							code := "other"
							switch {
							case errors.Is(e, ErrSandboxTimeout):
								code = "timeout"
							case errors.Is(e, ErrRuleExecution):
								code = "rule"
							case errors.Is(e, ErrSandboxOutputLimit):
								code = "output"
							}
							failures[code]++
						}
						mu.Unlock()
					}
				}()
			}
			wg.Wait()
			elapsed := time.Since(start).Seconds()
			runtime.ReadMemStats(&after)
			row := map[string]any{"mode": mode, "workload": w.name, "concurrency": concurrent, "attempts": len(all), "success": len(good), "errors": failures, "seconds": elapsed, "attempts_per_second": float64(len(all)) / elapsed, "success_per_second": float64(len(good)) / elapsed, "p50_success_ms": performancePercentile(good, .5), "p95_success_ms": performancePercentile(good, .95), "p99_success_ms": performancePercentile(good, .99), "p95_all_ms": performancePercentile(all, .95), "allocated_bytes": after.TotalAlloc - before.TotalAlloc, "gc_cycles": after.NumGC - before.NumGC}
			b, _ := json.Marshal(row)
			fmt.Println("WORKER_PERF " + string(b))
			if failures["other"] > 0 {
				t.Error("unexpected correctness failure", w.name)
			}
		}
	}
	if mode == "remote" && os.Getenv("TEST_WORKER_ADMISSION") == "1" {
		pattern := "\"<&>\\\n中文\u2028"
		escapeData := strings.Repeat(pattern, (1<<20)/len(pattern)+1)[:1<<20]
		// Bound admission pressure by the number of real workers: a single
		// worker cannot execute four large calls concurrently, and queueing
		// behind one slot would measure saturation rather than the budget.
		workers := 1
		if sockets := os.Getenv("TEST_WORKER_SOCKETS"); sockets != "" {
			workers = strings.Count(sockets, ",") + 1
		}
		concurrency := min(workers, 4)
		admit := func(name string, subject *Sandbox, data, script string, conc, wantSuccess int, strict bool) {
			inputSize, sizeErr := validateInput(context.Background(), map[string]any{"data": data}, subject.cfg.InputLimit)
			if sizeErr != nil {
				t.Fatalf("%s: input size: %v", name, sizeErr)
			}
			expected := "ok"
			if script == `data.length.toString()` {
				// JS string.length counts UTF-16 code units, not UTF-8 bytes.
				expected = strconv.Itoa(len(utf16.Encode([]rune(data))))
			}
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			all, good := []float64{}, []float64{}
			failures := map[string]int{}
			var mu sync.Mutex
			start := time.Now()
			var wg sync.WaitGroup
			for i := 0; i < conc; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					s := time.Now()
					out, e := subject.Eval(context.Background(), script, map[string]any{"data": data})
					ms := float64(time.Since(s).Nanoseconds()) / 1e6
					mu.Lock()
					all = append(all, ms)
					if e == nil && out == expected {
						good = append(good, ms)
					} else {
						code := "other"
						if errors.Is(e, ErrSandboxTimeout) {
							code = "timeout"
						}
						failures[code]++
					}
					mu.Unlock()
				}()
			}
			wg.Wait()
			runtime.ReadMemStats(&after)
			row := map[string]any{"mode": mode, "case": name, "concurrency": conc, "input_size_bytes": inputSize, "expected_success": wantSuccess, "strict": strict, "inflight_budget_bytes": subject.cfg.InflightBytes, "inflight_capacity_bytes": subject.remote.inflight.capacity, "remote_cfg_inflight_bytes": subject.remote.cfg.InflightBytes, "attempts": len(all), "success": len(good), "errors": failures, "seconds": time.Since(start).Seconds(), "attempts_ms": all, "p95_success_ms": performancePercentile(good, .95), "allocated_bytes": after.TotalAlloc - before.TotalAlloc, "parent_cgroup_peak_bytes": parentMemoryPeak()}
			b, _ := json.Marshal(row)
			fmt.Println("WORKER_ADMISSION " + string(b))
			if failures["other"] > 0 {
				t.Error("unexpected admission failure", name)
			}
			if strict && len(good) != wantSuccess {
				t.Errorf("%s admitted %d calls, want %d (budget %d bytes)", name, len(good), wantSuccess, subject.cfg.InflightBytes)
			}
			if strict && failures["timeout"] != conc-wantSuccess {
				t.Errorf("%s refused %d calls with timeout, want %d", name, failures["timeout"], conc-wantSuccess)
			}
		}
		// Production budget: multibyte input is encoded conservatively but all
		// workers fit, proving the escape overhead does not serialize admission.
		admit("escape-1MiB", sb, escapeData, `data.length.toString()`, concurrency, concurrency, true)
		// Tight budget: 4 MiB encodes to roughly 8 MiB, so a 10 MiB budget
		// admits one call. The admitted call holds its bytes for longer than
		// QueueTimeout, so the remaining callers are refused instead of being
		// admitted sequentially as short evaluations complete.
		tightPaths := strings.Split(os.Getenv("TEST_WORKER_SOCKETS"), ",")
		tight, err := NewRemote(Config{InputLimit: 5 << 20}, slowHTTP{600 * time.Millisecond}, tightPaths)
		if err != nil {
			t.Fatal(err)
		}
		if err := tight.Probe(context.Background()); err != nil {
			t.Fatal(err)
		}
		admit("tight-budget-4MiB", tight, strings.Repeat("x", 4<<20), `java.get("https://synthetic.invalid/")`, concurrency, 1, true)
		// Production budget under 12 MiB demand: at most two calls can hold
		// encoded copies at once; the rest are refused in the queue window.
		// One-CPU parents may also hit the 3s script deadline here, so this row
		// records the boundary without asserting an exact success count.
		admit("pressure-12MiB", sb, strings.Repeat("x", 12<<20), `data.length.toString()`, concurrency, 1, false)
		if out, err := sb.Eval(context.Background(), `"recovered"`, nil); err != nil || out != "recovered" {
			t.Fatal("sandbox unusable after admission pressure", out, err)
		}
	}
	if mode == "remote" && os.Getenv("TEST_WORKER_EDGES") == "1" {
		for _, mib := range []int{4, 8, 16, 31, 32} {
			data := strings.Repeat("x", mib<<20)
			s := time.Now()
			out, e := sb.Eval(context.Background(), `data.length.toString()`, map[string]any{"data": data})
			ok := e == nil && out == strconv.Itoa(len(data))
			data = ""
			recovered := false
			for until := time.Now().Add(8 * time.Second); time.Now().Before(until); {
				if sb.Probe(context.Background()) == nil {
					v, er := sb.Eval(context.Background(), `"recovered"`, nil)
					if er == nil && v == "recovered" {
						recovered = true
						break
					}
				}
				time.Sleep(100 * time.Millisecond)
			}
			code := ""
			if e != nil {
				code = e.Error()
			}
			b, _ := json.Marshal(map[string]any{"mode": mode, "edge_mib": mib, "accepted_correctly": ok, "error": code, "seconds_with_recovery": time.Since(s).Seconds(), "recovered": recovered})
			fmt.Println("WORKER_EDGE " + string(b))
			if !recovered {
				t.Fatal("new calls did not recover", mib)
			}
			if e == nil && !ok {
				t.Fatal("wrong edge result", mib)
			}
		}
	}
	if peak, e := os.ReadFile("/sys/fs/cgroup/memory.peak"); e == nil {
		fmt.Println("PARENT_CGROUP_PEAK " + strings.TrimSpace(string(peak)))
	}
}
