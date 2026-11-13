package cache

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"time"
)

// Redis 是最小 RESP 客户端，仅实现正文缓存所需的 GET / SETEX。
// 每次调用建立一次短连接（缓存访问频率低、超时短），保持实现简单可靠。
type Redis struct {
	addr      string
	timeout   time.Duration
	username  string
	password  string
	database  int
	tlsConfig *tls.Config
}

// NewRedis 创建 Redis 缓存客户端。
func NewRedis(addr string, timeout time.Duration) *Redis {
	if timeout <= 0 {
		timeout = 800 * time.Millisecond
	}
	return &Redis{addr: addr, timeout: timeout}
}

// Get 读取键；Redis nil 回复视为未命中。
func (r *Redis) Get(ctx context.Context, key string) ([]byte, error) {
	conn, err := r.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := writeCommand(conn, "GET", key); err != nil {
		return nil, err
	}
	// 必须复用同一个 bufio.Reader：首个 ReadString 可能已把 value 预读进缓冲，
	// 若重新构造 reader 会丢失缓冲区内字节，导致读到截断内容。
	br := bufReader(conn)
	line, err := readLine(br)
	if err != nil {
		return nil, err
	}
	switch line[0] {
	case '-':
		return nil, fmt.Errorf("redis: %s", line[1:])
	case '$':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, fmt.Errorf("redis: bad bulk length %q", line[1:])
		}
		if n < 0 {
			return nil, ErrMiss
		}
		buf := make([]byte, n+2)
		if _, err := readFull(br, buf); err != nil {
			return nil, err
		}
		return buf[:n], nil
	default:
		return nil, fmt.Errorf("redis: unexpected reply %q", line)
	}
}

// SetEX 写入键并设置 TTL。
func (r *Redis) SetEX(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	conn, err := r.dial(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	secs := int64(ttl / time.Second)
	if secs < 1 {
		secs = 1
	}
	if err := writeBulkCommand(conn, []string{"SETEX", key, strconv.FormatInt(secs, 10)}, value); err != nil {
		return err
	}
	line, err := readLine(bufReader(conn))
	if err != nil {
		return err
	}
	if len(line) > 0 && line[0] == '-' {
		return fmt.Errorf("redis: %s", line[1:])
	}
	return nil
}

// Keys 返回匹配 pattern 的键（运营/测试核对用，生产代码不应在请求路径使用）。
func (r *Redis) Keys(ctx context.Context, pattern string) ([]string, error) {
	conn, err := r.dial(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	if err := writeCommand(conn, "KEYS", pattern); err != nil {
		return nil, err
	}
	br := bufReader(conn)
	line, err := readLine(br)
	if err != nil {
		return nil, err
	}
	switch line[0] {
	case '-':
		return nil, fmt.Errorf("redis: %s", line[1:])
	case '*':
		n, err := strconv.Atoi(line[1:])
		if err != nil {
			return nil, fmt.Errorf("redis: bad array length %q", line[1:])
		}
		out := make([]string, 0, n)
		for i := 0; i < n; i++ {
			v, err := readBulk(br)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("redis: unexpected reply %q", line)
	}
}

// TTL 返回键剩余生存时间：键不存在返回 ErrMiss，未设置过期返回 -1。
func (r *Redis) TTL(ctx context.Context, key string) (time.Duration, error) {
	conn, err := r.dial(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	if err := writeCommand(conn, "TTL", key); err != nil {
		return 0, err
	}
	line, err := readLine(bufReader(conn))
	if err != nil {
		return 0, err
	}
	if line[0] == '-' {
		return 0, fmt.Errorf("redis: %s", line[1:])
	}
	if line[0] != ':' {
		return 0, fmt.Errorf("redis: unexpected reply %q", line)
	}
	secs, err := strconv.ParseInt(line[1:], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("redis: bad integer reply %q", line[1:])
	}
	switch secs {
	case -1:
		return -1, nil
	case -2:
		return 0, ErrMiss
	default:
		return time.Duration(secs) * time.Second, nil
	}
}

// Delete 删除一个或多个键（测试清理与运维用），返回实际删除的数量。
func (r *Redis) Delete(ctx context.Context, keys ...string) (int64, error) {
	if len(keys) == 0 {
		return 0, nil
	}
	conn, err := r.dial(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()

	if err := writeCommand(conn, append([]string{"DEL"}, keys...)...); err != nil {
		return 0, err
	}
	line, err := readLine(bufReader(conn))
	if err != nil {
		return 0, err
	}
	if line[0] == '-' {
		return 0, fmt.Errorf("redis: %s", line[1:])
	}
	if line[0] != ':' {
		return 0, fmt.Errorf("redis: unexpected reply %q", line)
	}
	return strconv.ParseInt(line[1:], 10, 64)
}

func readBulk(br *bufio.Reader) (string, error) {
	line, err := readLine(br)
	if err != nil {
		return "", err
	}
	if line[0] == '-' {
		return "", fmt.Errorf("redis: %s", line[1:])
	}
	if line[0] != '$' {
		return "", fmt.Errorf("redis: unexpected bulk reply %q", line)
	}
	n, err := strconv.Atoi(line[1:])
	if err != nil {
		return "", fmt.Errorf("redis: bad bulk length %q", line[1:])
	}
	if n < 0 {
		return "", nil
	}
	buf := make([]byte, n+2)
	if _, err := readFull(br, buf); err != nil {
		return "", err
	}
	return string(buf[:n]), nil
}

func (r *Redis) dial(ctx context.Context) (net.Conn, error) {
	d := net.Dialer{Timeout: r.timeout}
	var conn net.Conn
	var err error
	if r.tlsConfig != nil {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: r.tlsConfig}).DialContext(ctx, "tcp", r.addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", r.addr)
	}
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(r.timeout)
	if limit, ok := ctx.Deadline(); ok && limit.Before(deadline) {
		deadline = limit
	}
	_ = conn.SetDeadline(deadline)
	br := bufReader(conn)
	setup := func(args ...string) error {
		if err := writeCommand(conn, args...); err != nil {
			return err
		}
		line, err := readLine(br)
		if err != nil {
			return err
		}
		if line != "+OK" {
			return fmt.Errorf("Redis connection setup rejected")
		}
		return nil
	}
	if r.password != "" {
		args := []string{"AUTH"}
		if r.username != "" {
			args = append(args, r.username)
		}
		args = append(args, r.password)
		if err := setup(args...); err != nil {
			conn.Close()
			return nil, err
		}
	}
	if r.database != 0 {
		if err := setup("SELECT", strconv.Itoa(r.database)); err != nil {
			conn.Close()
			return nil, err
		}
	}
	return conn, nil
}

func bufReader(conn net.Conn) *bufio.Reader { return bufio.NewReaderSize(conn, 64*1024) }

func readLine(br *bufio.Reader) (string, error) {
	line, err := br.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(line) < 3 || line[len(line)-2] != '\r' {
		return "", fmt.Errorf("redis: malformed line %q", line)
	}
	return line[:len(line)-2], nil
}

func readFull(br *bufio.Reader, buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		m, err := br.Read(buf[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func writeCommand(conn net.Conn, args ...string) error {
	var b []byte
	b = append(b, '*')
	b = strconv.AppendInt(b, int64(len(args)), 10)
	b = append(b, '\r', '\n')
	for _, a := range args {
		b = append(b, '$')
		b = strconv.AppendInt(b, int64(len(a)), 10)
		b = append(b, '\r', '\n')
		b = append(b, a...)
		b = append(b, '\r', '\n')
	}
	_, err := conn.Write(b)
	return err
}

func writeBulkCommand(conn net.Conn, args []string, payload []byte) error {
	var b []byte
	b = append(b, '*')
	b = strconv.AppendInt(b, int64(len(args)+1), 10)
	b = append(b, '\r', '\n')
	for _, a := range args {
		b = append(b, '$')
		b = strconv.AppendInt(b, int64(len(a)), 10)
		b = append(b, '\r', '\n')
		b = append(b, a...)
		b = append(b, '\r', '\n')
	}
	b = append(b, '$')
	b = strconv.AppendInt(b, int64(len(payload)), 10)
	b = append(b, '\r', '\n')
	b = append(b, payload...)
	b = append(b, '\r', '\n')
	_, err := conn.Write(b)
	return err
}
