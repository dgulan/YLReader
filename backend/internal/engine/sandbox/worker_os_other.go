//go:build !linux

package sandbox

import "net"

func checkIPCSocket(string) error                             { return ErrWorkerUnavailable }
func listenWorkerSocket(string) (net.Listener, func(), error) { return nil, nil, ErrWorkerUnavailable }
func workerIsolation() (workerHello, error)                   { return workerHello{}, ErrWorkerUnavailable }
