//go:build !linux

package browserassist

func privateRelaySocket(string) error { return ErrScope }
