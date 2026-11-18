package cache

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

func identityReply(s string) string { return fmt.Sprintf("$%d\r\n%s\r\n", len(s), s) }

func TestRedisIdentityObservationCommandsAndBinding(t *testing.T) {
	id := strings.Repeat("a", 40)
	info := identityReply("# Server\r\nredis_mode:standalone\r\nrun_id:" + id + "\r\n")
	repl := identityReply("role:master\r\nconnected_slaves:0\r\n")
	key := "ylreader:handover:00000000-0000-4000-8000-000000000001"
	commands := make(chan []string, 7)
	addr := fakeRedis(t, []string{"+OK\r\n", "+OK\r\n", info, repl, identityReply(strings.Repeat("w", 32)), info, repl}, commands)
	r, e := ConfiguredRedis("redis://user:password@"+addr+"/2", "", time.Second)
	if e != nil {
		t.Fatal(e)
	}
	seen, e := r.ObserveServerWitness(context.Background(), key)
	if e != nil || seen.RunID != id || seen.Database != 2 || !bytes.Equal(seen.Witness, bytes.Repeat([]byte("w"), 32)) {
		t.Fatal("identity mismatch", e)
	}
	for _, want := range [][]string{{"AUTH", "user", "password"}, {"SELECT", "2"}, {"INFO", "server"}, {"INFO", "replication"}, {"GET", key}, {"INFO", "server"}, {"INFO", "replication"}} {
		select {
		case got := <-commands:
			if !reflect.DeepEqual(got, want) {
				t.Fatal("unexpected observation command")
			}
		case <-time.After(time.Second):
			t.Fatal("command missing")
		}
	}
}

func TestRedisIdentityBoundedProtocolAndDrift(t *testing.T) {
	id := strings.Repeat("a", 40)
	info := identityReply("redis_mode:standalone\r\nrun_id:" + id + "\r\n")
	repl := identityReply("role:master\r\nconnected_slaves:0\r\n")
	for name, replies := range map[string][]string{
		"permission":          {"-NOPERM SECRET\r\n"},
		"oversize_info":       {"$65537\r\n"},
		"bad_info_terminator": {"$3\r\nabcxx"},
		"missing_id":          {identityReply("# Server\r\n")},
		"duplicate_id":        {identityReply("run_id:" + id + "\r\nrun_id:" + id + "\r\n")},
		"invalid_id":          {identityReply("run_id:SECRET\r\n")},
		"missing_witness":     {info, repl, "$-1\r\n"},
		"oversize_witness":    {info, repl, "$65\r\n"},
		"cluster_mode":        {identityReply("redis_mode:cluster\r\nrun_id:" + id + "\r\n")},
		"replica":             {info, identityReply("role:slave\r\nconnected_slaves:0\r\n")},
		"has_replica":         {info, identityReply("role:master\r\nconnected_slaves:1\r\n")},
		"id_drift":            {info, repl, identityReply(strings.Repeat("w", 32)), identityReply("redis_mode:standalone\r\nrun_id:" + strings.Repeat("b", 40) + "\r\n"), repl},
	} {
		t.Run(name, func(t *testing.T) {
			r := NewRedis(fakeRedis(t, replies, nil), time.Second)
			seen, e := r.ObserveServerWitness(context.Background(), "ylreader:handover:00000000-0000-4000-8000-000000000001")
			if e != ErrIdentityObservation || seen.RunID != "" || len(seen.Witness) != 0 {
				t.Fatal("malformed/changed observation accepted or raw error exposed")
			}
		})
	}
	for _, raw := range []string{"$9999999999999999999999999\r\n", "+OK\r\n", "$1\r\nxXX", "$-1\r\n"} {
		if _, e := identityBulk(bufio.NewReader(strings.NewReader(raw)), 64); e == nil {
			t.Fatal("bad bulk accepted")
		}
	}
}

func TestRedisIdentityInvalidInputAndCancellation(t *testing.T) {
	r := NewRedis("127.0.0.1:1", time.Second)
	if _, e := r.ObserveServerWitness(context.Background(), "other-key"); e != ErrIdentityObservation {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := r.ObserveServerWitness(ctx, "ylreader:handover:00000000-0000-4000-8000-000000000001"); e != ErrIdentityObservation {
		t.Fatal(e)
	}
	if _, e := r.ObserveServerWitness(nil, "x"); e != ErrIdentityObservation {
		t.Fatal(e)
	}
}
