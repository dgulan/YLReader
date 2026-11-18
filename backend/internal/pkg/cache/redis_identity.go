package cache

import (
	"bufio"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
)

var ErrIdentityObservation = errors.New("redis: identity observation unavailable")
var handoverWitnessKey = regexp.MustCompile(`^ylreader:handover:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type ServerWitness struct {
	RunID    string
	Database int
	Witness  []byte
}

func identityBulk(br *bufio.Reader, limit int) ([]byte, error) {
	header, e := br.ReadSlice('\n')
	if e != nil || len(header) < 4 || len(header) > 64 || header[0] != '$' || header[len(header)-2] != '\r' {
		return nil, ErrIdentityObservation
	}
	n, e := strconv.Atoi(string(header[1 : len(header)-2]))
	if e != nil || n < 0 || n > limit {
		return nil, ErrIdentityObservation
	}
	data := make([]byte, n+2)
	if _, e = io.ReadFull(br, data); e != nil || data[n] != '\r' || data[n+1] != '\n' {
		return nil, ErrIdentityObservation
	}
	return data[:n], nil
}

func serverRunID(info []byte) (string, error) {
	id := ""
	mode := ""
	for _, line := range strings.Split(string(info), "\r\n") {
		if strings.HasPrefix(line, "redis_mode:") {
			if mode != "" {
				return "", ErrIdentityObservation
			}
			mode = strings.TrimPrefix(line, "redis_mode:")
			if mode != "standalone" {
				return "", ErrIdentityObservation
			}
		}
		if !strings.HasPrefix(line, "run_id:") {
			continue
		}
		if id != "" {
			return "", ErrIdentityObservation
		}
		id = strings.TrimPrefix(line, "run_id:")
		b, e := hex.DecodeString(id)
		if e != nil || len(b) != 20 || hex.EncodeToString(b) != id {
			return "", ErrIdentityObservation
		}
	}
	if id == "" || mode != "standalone" {
		return "", ErrIdentityObservation
	}
	return id, nil
}

func standaloneMaster(info []byte) bool {
	fields := map[string]string{}
	for _, line := range strings.Split(string(info), "\r\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok || (key != "role" && key != "connected_slaves") {
			continue
		}
		if _, seen := fields[key]; seen {
			return false
		}
		fields[key] = value
	}
	return fields["role"] == "master" && fields["connected_slaves"] == "0"
}

// ObserveServerWitness only reads one scoped key and server metadata. The
// witness must already exist; no TTL refresh, write, cleanup or key scan occurs.
func (r *Redis) ObserveServerWitness(ctx context.Context, key string) (ServerWitness, error) {
	if r == nil || ctx == nil || !handoverWitnessKey.MatchString(key) {
		return ServerWitness{}, ErrIdentityObservation
	}
	c, e := r.dial(ctx)
	if e != nil {
		return ServerWitness{}, ErrIdentityObservation
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { _ = c.Close() })
	defer stop()
	br := bufReader(c)
	readID := func() (string, error) {
		if writeCommand(c, "INFO", "server") != nil {
			return "", ErrIdentityObservation
		}
		data, e := identityBulk(br, 64<<10)
		if e != nil {
			return "", ErrIdentityObservation
		}
		id, e := serverRunID(data)
		if e != nil || writeCommand(c, "INFO", "replication") != nil {
			return "", ErrIdentityObservation
		}
		data, e = identityBulk(br, 64<<10)
		if e != nil || !standaloneMaster(data) {
			return "", ErrIdentityObservation
		}
		return id, nil
	}
	first, e := readID()
	if e != nil {
		return ServerWitness{}, ErrIdentityObservation
	}
	if writeCommand(c, "GET", key) != nil {
		return ServerWitness{}, ErrIdentityObservation
	}
	witness, e := identityBulk(br, 64)
	if e != nil {
		return ServerWitness{}, ErrIdentityObservation
	}
	last, e := readID()
	if e != nil || first != last || ctx.Err() != nil {
		clear(witness)
		return ServerWitness{}, ErrIdentityObservation
	}
	return ServerWitness{first, r.database, witness}, nil
}
