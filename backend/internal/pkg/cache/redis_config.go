package cache

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ConfiguredRedis returns one client for both cache operations and readiness.
// REDIS_ADDR remains a compatibility alias; conflicting endpoints fail closed.
func ConfiguredRedis(rawURL, legacyAddr string, timeout time.Duration) (*Redis, error) {
	rawURL, legacyAddr = strings.TrimSpace(rawURL), strings.TrimSpace(legacyAddr)
	if rawURL == "" && legacyAddr == "" {
		return nil, nil
	}
	if rawURL == "" {
		rawURL = "redis://" + legacyAddr
	}
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "redis" && u.Scheme != "rediss") || u.Hostname() == "" || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid Redis URL configuration")
	}
	port := u.Port()
	if port == "" {
		port = "6379"
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return nil, errors.New("invalid Redis port")
	}
	addr := net.JoinHostPort(strings.ToLower(u.Hostname()), port)
	if legacyAddr != "" {
		legacy, err := url.Parse("redis://" + legacyAddr)
		if err != nil || legacy.Hostname() == "" || legacy.User != nil || legacy.Path != "" || legacy.RawQuery != "" || legacy.Fragment != "" {
			return nil, errors.New("invalid REDIS_ADDR")
		}
		p := legacy.Port()
		if p == "" {
			p = "6379"
		}
		if net.JoinHostPort(strings.ToLower(legacy.Hostname()), p) != addr {
			return nil, errors.New("REDIS_URL and REDIS_ADDR endpoints disagree")
		}
	}
	r := NewRedis(addr, timeout)
	if u.Path != "" && u.Path != "/" {
		db, err := strconv.Atoi(strings.TrimPrefix(u.Path, "/"))
		if err != nil || db < 0 {
			return nil, errors.New("invalid Redis database index")
		}
		r.database = db
	}
	if u.User != nil {
		r.username = u.User.Username()
		r.password, _ = u.User.Password()
		if r.password == "" {
			return nil, errors.New("Redis authentication requires a password")
		}
	}
	if u.Scheme == "rediss" {
		r.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname()}
	}
	return r, nil
}

func (r *Redis) Ping(ctx context.Context) error {
	conn, err := r.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := writeCommand(conn, "PING"); err != nil {
		return err
	}
	line, err := readLine(bufReader(conn))
	if err != nil {
		return err
	}
	if line != "+PONG" {
		return fmt.Errorf("Redis PING did not return PONG")
	}
	return nil
}
