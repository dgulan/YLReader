package main

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServeUntilCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	srv := &http.Server{Addr: "127.0.0.1:0"}
	done := make(chan error, 1)
	go func() { done <- serveUntilCanceled(ctx, srv) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
}

func TestServeReportsListenFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	srv := &http.Server{Addr: listener.Addr().String()}
	if err := serveUntilCanceled(context.Background(), srv); err == nil {
		t.Fatal("expected occupied port error")
	}
}
