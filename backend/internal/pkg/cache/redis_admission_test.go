package cache

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRedisDomainAdmissionProtocolAndConfiguration(t *testing.T) {
	for _, reply := range []string{":0\r\n", ":240\r\n", ":-1\r\n", ":501\r\n", ":oops\r\n", "-ERR private server details\r\n", "+OK\r\n", "\r\n"} {
		t.Run(strings.TrimSpace(reply), func(t *testing.T) {
			commands := make(chan []string, 3)
			addr := fakeRedis(t, []string{"+OK\r\n", "+OK\r\n", reply}, commands)
			r, err := ConfiguredRedis("redis://reader:test-only@"+addr+"/2", "", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			delay, err := r.Take(context.Background(), "test-key", 500*time.Millisecond)
			valid := reply == ":0\r\n" || reply == ":240\r\n"
			if (err == nil) != valid {
				t.Fatal(delay, err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("server error leaked")
			}
			if reply == ":240\r\n" && delay != 240*time.Millisecond {
				t.Fatal(delay)
			}
			if args := <-commands; strings.Join(args, ",") != "AUTH,reader,test-only" {
				t.Fatal(args)
			}
			if args := <-commands; strings.Join(args, ",") != "SELECT,2" {
				t.Fatal(args)
			}
			args := <-commands
			if len(args) != 5 || args[0] != "EVAL" || args[1] != domainAdmissionScript || args[2] != "1" || args[3] != "test-key" || args[4] != "500" {
				t.Fatal("wrong atomic command")
			}
		})
	}
}

// Only point TEST_REDIS_ADDR at an isolated disposable test Redis.
func TestRedisDomainAdmissionIntegration(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("requires isolated TEST_REDIS_ADDR")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	r := NewRedis(addr, time.Second)
	key := fmt.Sprintf("yreader:test:admission:%d", time.Now().UnixNano())
	defer r.Delete(context.Background(), key, key+":independent", key+":corrupt")
	var wg sync.WaitGroup
	var admitted atomic.Int32
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Independent client instances share the atomic Redis budget.
			d, err := NewRedis(addr, time.Second).Take(ctx, key, 5*time.Second)
			if err != nil {
				t.Error(err)
			} else if d == 0 {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 1 {
		t.Fatal("concurrent admissions", admitted.Load())
	}
	if d, err := r.Take(ctx, key+":independent", 5*time.Millisecond); err != nil || d != 0 {
		t.Fatal(d, err)
	}
	time.Sleep(20 * time.Millisecond)
	if d, err := r.Take(ctx, key+":independent", 5*time.Millisecond); err != nil || d != 0 {
		t.Fatal("expired slot not released", d, err)
	}
	conn, err := r.dial(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeCommand(conn, "SET", key+":corrupt", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := readLine(bufReader(conn)); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if _, err := r.Take(ctx, key+":corrupt", time.Second); err == nil {
		t.Fatal("persistent slot admitted")
	}
}
