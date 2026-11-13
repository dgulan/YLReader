package cache

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRedisConfiguration(t *testing.T) {
	r, err := ConfiguredRedis("", "", time.Second)
	if err != nil || r != nil {
		t.Fatal("expected disabled Redis")
	}
	r, err = ConfiguredRedis("redis://user:p%40ss@localhost:6379/2", "localhost:6379", time.Second)
	if err != nil || r.database != 2 || r.password != "p@ss" || r.username != "user" {
		t.Fatalf("URL not applied: %v", err)
	}
	for _, input := range [][2]string{{"redis://localhost:6379", "localhost:6380"}, {"http://localhost:6379", ""}, {"redis://localhost/-1", ""}, {"redis://localhost/0?ignored=1", ""}, {"redis://user:SECRET@localhost:99999/0", ""}} {
		_, err := ConfiguredRedis(input[0], input[1], time.Second)
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Fatalf("unsafe accepted configuration: %v", err)
		}
	}
	r, err = ConfiguredRedis("", "127.0.0.1:6379", time.Second)
	if err != nil || r.addr != "127.0.0.1:6379" {
		t.Fatal(err)
	}
	r, err = ConfiguredRedis("rediss://localhost/0", "", time.Second)
	if err != nil || r.tlsConfig == nil {
		t.Fatal("TLS not configured")
	}
}

func fakeRedis(t *testing.T, replies []string, commands chan<- []string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		conn.SetDeadline(time.Now().Add(3 * time.Second))
		br := bufio.NewReader(conn)
		for _, reply := range replies {
			var count int
			line, err := readLine(br)
			if err != nil {
				return
			}
			if _, err = fmt.Sscanf(line, "*%d", &count); err != nil {
				return
			}
			args := make([]string, 0, count)
			for i := 0; i < count; i++ {
				value, err := readBulk(br)
				if err != nil {
					return
				}
				args = append(args, value)
			}
			if commands != nil {
				commands <- args
			}
			if _, err = conn.Write([]byte(reply)); err != nil {
				return
			}
		}
	}()
	return listener.Addr().String()
}

func TestRedisPingRequiresPong(t *testing.T) {
	for _, reply := range []string{"+PONG\r\n", "-NOAUTH Authentication required\r\n", "+OK\r\n", "garbage\r\n", "\r\n"} {
		t.Run(strings.TrimSpace(reply), func(t *testing.T) {
			r := NewRedis(fakeRedis(t, []string{reply}, nil), time.Second)
			err := r.Ping(context.Background())
			if (err == nil) != (reply == "+PONG\r\n") {
				t.Fatalf("wrong probe result: %v", err)
			}
		})
	}
}

func TestRedisAuthAndDatabaseUsedForPingAndCache(t *testing.T) {
	for _, operation := range []string{"PING", "GET", "SETEX"} {
		t.Run(operation, func(t *testing.T) {
			last := "+PONG\r\n"
			if operation == "GET" {
				last = "$5\r\nvalue\r\n"
			}
			if operation == "SETEX" {
				last = "+OK\r\n"
			}
			commands := make(chan []string, 3)
			addr := fakeRedis(t, []string{"+OK\r\n", "+OK\r\n", last}, commands)
			r, err := ConfiguredRedis("redis://reader:p%40ss@"+addr+"/2", "", time.Second)
			if err != nil {
				t.Fatal(err)
			}
			switch operation {
			case "PING":
				err = r.Ping(context.Background())
			case "GET":
				var value []byte
				value, err = r.Get(context.Background(), "key")
				if string(value) != "value" {
					t.Fatal("wrong value")
				}
			case "SETEX":
				err = r.SetEX(context.Background(), "key", []byte("value"), time.Minute)
			}
			if err != nil {
				t.Fatal(err)
			}
			if args := <-commands; strings.Join(args, ",") != "AUTH,reader,p@ss" {
				t.Fatalf("AUTH command: %v", args)
			}
			if args := <-commands; strings.Join(args, ",") != "SELECT,2" {
				t.Fatalf("SELECT command: %v", args)
			}
			if args := <-commands; args[0] != operation {
				t.Fatalf("command: %v", args)
			}
		})
	}
}
