//go:build linux

package sandbox

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

func privateIPCDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ErrWorkerUnavailable
	}
	parent := filepath.Dir(path)
	for current := parent; ; current = filepath.Dir(current) {
		st, err := os.Lstat(current)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return ErrWorkerUnavailable
		}
		if current == parent {
			sys, ok := st.Sys().(*syscall.Stat_t)
			if !ok || sys.Uid != uint32(os.Geteuid()) || st.Mode().Perm() != 0700 {
				return ErrWorkerUnavailable
			}
		}
		if current == "/" {
			break
		}
	}
	return nil
}
func checkIPCSocket(path string) error {
	if privateIPCDirectory(path) != nil {
		return ErrWorkerUnavailable
	}
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&os.ModeSocket == 0 || st.Mode()&os.ModeSymlink != 0 || st.Mode().Perm() != 0600 {
		return ErrWorkerUnavailable
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || sys.Uid != uint32(os.Geteuid()) {
		return ErrWorkerUnavailable
	}
	return nil
}
func listenWorkerSocket(path string) (net.Listener, func(), error) {
	if privateIPCDirectory(path) != nil {
		return nil, nil, ErrWorkerUnavailable
	}
	fd, err := unix.Open(filepath.Join(filepath.Dir(path), ".worker.lock"), unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, nil, ErrWorkerUnavailable
	}
	lock := os.NewFile(uintptr(fd), "worker-lock")
	fail := func() (net.Listener, func(), error) { lock.Close(); return nil, nil, ErrWorkerUnavailable }
	st, err := lock.Stat()
	if err != nil {
		return fail()
	}
	sys, ok := st.Sys().(*syscall.Stat_t)
	if !ok || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || sys.Uid != uint32(os.Geteuid()) || sys.Nlink != 1 || unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB) != nil {
		return fail()
	}
	if st, err := os.Lstat(path); err == nil {
		if st.Mode()&os.ModeSocket == 0 || checkIPCSocket(path) != nil {
			return fail()
		}
		if os.Remove(path) != nil {
			return fail()
		}
	} else if !os.IsNotExist(err) {
		return fail()
	}
	l, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return fail()
	}
	if os.Chmod(path, 0600) != nil {
		l.Close()
		return fail()
	}
	return l, func() { l.Close(); lock.Close() }, nil
}

func workerIsolation() (workerHello, error) {
	fail := func() (workerHello, error) { return workerHello{}, ErrWorkerUnavailable }
	if os.Geteuid() == 0 {
		return fail()
	}
	read := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 65536 {
			return ""
		}
		return strings.TrimSpace(string(data))
	}
	if read("/proc/self/cgroup") != "0::/" {
		return fail()
	}
	status := read("/proc/self/status")
	fields := map[string]string{}
	for _, line := range strings.Split(status, "\n") {
		if i := strings.IndexByte(line, ':'); i >= 0 {
			fields[line[:i]] = strings.TrimSpace(line[i+1:])
		}
	}
	if fields["NoNewPrivs"] != "1" || fields["Seccomp"] != "2" {
		return fail()
	}
	for _, name := range []string{"CapEff", "CapPrm", "CapInh", "CapBnd", "CapAmb"} {
		cap, err := strconv.ParseUint(fields[name], 16, 64)
		if err != nil || cap != 0 {
			return fail()
		}
	}
	memory, err := strconv.ParseInt(read("/sys/fs/cgroup/memory.max"), 10, 64)
	if err != nil || memory <= 0 || memory > 256<<20 || read("/sys/fs/cgroup/memory.swap.max") != "0" {
		return fail()
	}
	pids, err := strconv.Atoi(read("/sys/fs/cgroup/pids.max"))
	if err != nil || pids < 1 || pids > 64 {
		return fail()
	}
	cpu := strings.Fields(read("/sys/fs/cgroup/cpu.max"))
	if len(cpu) != 2 {
		return fail()
	}
	quota, e1 := strconv.ParseInt(cpu[0], 10, 64)
	period, e2 := strconv.ParseInt(cpu[1], 10, 64)
	if e1 != nil || e2 != nil || quota <= 0 || period <= 0 || period > 1000000 || quota > 2*period {
		return fail()
	}
	if read("/sys/fs/cgroup/cgroup.procs") != fmt.Sprint(os.Getpid()) {
		return fail()
	}
	var fs unix.Statfs_t
	if unix.Statfs("/", &fs) != nil || fs.Flags&unix.ST_RDONLY == 0 {
		return fail()
	}
	interfaces, err := net.Interfaces()
	if err != nil {
		return fail()
	}
	for _, network := range interfaces {
		if network.Flags&net.FlagLoopback == 0 {
			return fail()
		}
	}
	return workerHello{Profile: "cgroup-v2-v1", MemoryMax: memory}, nil
}
