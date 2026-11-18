// Package backup implements the ADR-019 isolated experiment. It has no server
// route registration and does not open production database connections.
package backup

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// syncTerminateConn waits boundedly for the server EOF after a PostgreSQL
// Terminate ('X') message so proc_exit/ProcKill completes before Close returns.
type syncTerminateConn struct {
	net.Conn
	terminated atomic.Bool
	once       sync.Once
	closeErr   error
}

func (c *syncTerminateConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil && len(p) >= 5 && bytes.Equal(p[len(p)-5:], []byte{'X', 0, 0, 0, 4}) {
		c.terminated.Store(true)
	}
	return n, err
}

func (c *syncTerminateConn) Close() error {
	c.once.Do(func() {
		if c.terminated.Load() {
			if cw, ok := c.Conn.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			_ = c.Conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			var buf [64]byte
			for {
				if _, err := c.Conn.Read(buf[:]); err != nil {
					break
				}
			}
		}
		c.closeErr = c.Conn.Close()
	})
	return c.closeErr
}

var ErrTargetDenied = errors.New("backup: isolated database target denied")
var isolatedDB = regexp.MustCompile(`^yreader_a1_iso_[a-z0-9_]{1,24}$`)

type targetDeniedError struct {
	stage string
}

func (e targetDeniedError) Error() string {
	return ErrTargetDenied.Error() + " (" + e.stage + ")"
}

func (e targetDeniedError) Unwrap() error {
	return ErrTargetDenied
}

func targetDeniedStageError(stage string) error {
	if stage == "" {
		return ErrTargetDenied
	}
	return targetDeniedError{stage: stage}
}

// ParseTarget accepts only explicit PostgreSQL URIs for newly provisioned
// isolated databases. It neither connects nor proves that a database is empty.
// No parser errors are returned verbatim: they can contain DSNs or passwords.
func ParseTarget(raw string) (*pgx.ConnConfig, error) {
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(key), "PG") {
			return nil, ErrTargetDenied
		}
	}
	if len(raw) > 16384 {
		return nil, ErrTargetDenied
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Fragment != "" || u.User == nil || u.User.Username() == "" {
		return nil, ErrTargetDenied
	}
	password, explicitPassword := u.User.Password()
	if !explicitPassword {
		return nil, ErrTargetDenied
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "::1" && host != "localhost" {
		return nil, ErrTargetDenied
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, ErrTargetDenied
	}
	if !isolatedDB.MatchString(strings.TrimPrefix(u.Path, "/")) || strings.Count(u.Path, "/") != 1 {
		return nil, ErrTargetDenied
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return nil, ErrTargetDenied
	}
	for key, values := range query {
		if len(values) != 1 {
			return nil, ErrTargetDenied
		}
		switch key {
		case "sslmode":
			if values[0] != "disable" && values[0] != "require" && values[0] != "verify-full" {
				return nil, ErrTargetDenied
			}
		case "connect_timeout":
			n, e := strconv.Atoi(values[0])
			if e != nil || n < 1 || n > 120 {
				return nil, ErrTargetDenied
			}
		case "application_name":
			if len(values[0]) > 64 {
				return nil, ErrTargetDenied
			}
		default:
			return nil, ErrTargetDenied
		}
	}
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		return nil, ErrTargetDenied
	}
	if cfg.Host != host || int(cfg.Port) != port || cfg.Database != strings.TrimPrefix(u.Path, "/") || cfg.User != u.User.Username() || cfg.Password != password || len(cfg.RuntimeParams) > 1 {
		return nil, ErrTargetDenied
	}
	// Pin localhost to a literal so a later DNS lookup cannot change the target.
	// Preserve TLS verification names already created by pgx.
	if host == "localhost" {
		cfg.Host = "127.0.0.1"
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback.Host != host || int(fallback.Port) != port {
			return nil, ErrTargetDenied
		}
		fallback.Host = cfg.Host
	}
	cfg.AfterNetConnect = func(_ context.Context, _ *pgconn.Config, conn net.Conn) (net.Conn, error) {
		return &syncTerminateConn{Conn: conn}, nil
	}
	return cfg, nil
}
