package cache

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestRedisSourceAdmissionRejectsCorruptState(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("requires isolated TEST_REDIS_ADDR")
	}
	r := NewRedis(addr, time.Second)
	ctx := context.Background()
	for _, fault := range []string{"wrong-type", "no-ttl", "nan"} {
		t.Run(fault, func(t *testing.T) {
			prefix := fmt.Sprintf("yreader:test:corrupt:%d:", time.Now().UnixNano())
			source, domain := prefix+"source", prefix+"domain"
			if d, err := r.TakeSource(ctx, domain, source, "first", 2, time.Second, time.Second); err != nil || d != 0 {
				t.Fatal(d, err)
			}
			defer r.Delete(ctx, domain, domain+"next", source+":meta", source+":leases")
			conn, err := r.dial(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			args := []string{"HSET", source + ":meta", "t", "nan"}
			if fault == "no-ttl" {
				args = []string{"PERSIST", source + ":meta"}
			}
			if fault == "wrong-type" {
				if err := writeCommand(conn, "DEL", source+":meta"); err != nil {
					t.Fatal(err)
				}
				if _, err := readLine(bufReader(conn)); err != nil {
					t.Fatal(err)
				}
				args = []string{"SET", source + ":meta", "corrupt", "PX", "1000"}
			}
			if err := writeCommand(conn, args...); err != nil {
				t.Fatal(err)
			}
			if _, err := readLine(bufReader(conn)); err != nil {
				t.Fatal(err)
			}
			if _, err := r.TakeSource(ctx, domain+"next", source, "next", 2, time.Second, time.Second); err == nil {
				t.Fatal("corrupt state admitted")
			}
			if d, err := r.Take(ctx, domain+"next", 500*time.Millisecond); err != nil || d != 0 {
				t.Fatal("corrupt rejection consumed domain", d, err)
			}
		})
	}
}
