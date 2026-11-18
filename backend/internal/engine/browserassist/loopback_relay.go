package browserassist

import (
	"context"
	"io"
	"net"
	"runtime"
	"sync"
	"time"
)

type LoopbackRelay struct {
	socket   string
	slots    chan struct{}
	lifetime time.Duration
}

func NewLoopbackRelay(socket string, connections int, lifetime time.Duration) (*LoopbackRelay, error) {
	if runtime.GOOS != "linux" || privateRelaySocket(socket) != nil {
		return nil, ErrScope
	}
	if connections == 0 {
		connections = 4
	}
	if lifetime == 0 {
		lifetime = 30 * time.Second
	}
	if connections < 1 || connections > 16 || lifetime < time.Millisecond || lifetime > 2*time.Minute {
		return nil, ErrScope
	}
	return &LoopbackRelay{socket: socket, slots: make(chan struct{}, connections), lifetime: lifetime}, nil
}
func (r *LoopbackRelay) Serve(parent context.Context, listener net.Listener) error {
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok || !address.IP.IsLoopback() {
		return ErrScope
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	var active sync.WaitGroup
	defer func() { cancel(); listener.Close(); active.Wait() }()
	for {
		client, e := listener.Accept()
		if e != nil {
			if ctx.Err() != nil {
				return nil
			}
			return ErrUnavailable
		}
		select {
		case r.slots <- struct{}{}:
		default:
			client.Close()
			continue
		}
		active.Add(1)
		go func() {
			defer active.Done()
			defer func() { <-r.slots }()
			defer client.Close()
			work, end := context.WithTimeout(ctx, r.lifetime)
			defer end()
			if privateRelaySocket(r.socket) != nil {
				return
			}
			upstream, e := (&net.Dialer{}).DialContext(work, "unix", r.socket)
			if e != nil {
				return
			}
			defer upstream.Close()
			deadline, _ := work.Deadline()
			client.SetDeadline(deadline)
			upstream.SetDeadline(deadline)
			closeBoth := func() { client.Close(); upstream.Close() }
			stop := context.AfterFunc(work, closeBoth)
			defer stop()
			done := make(chan struct{}, 2)
			go func() { io.Copy(upstream, io.LimitReader(client, 16<<20)); done <- struct{}{} }()
			go func() { io.Copy(client, io.LimitReader(upstream, 16<<20)); done <- struct{}{} }()
			<-done
			closeBoth()
			<-done
		}()
	}
}
