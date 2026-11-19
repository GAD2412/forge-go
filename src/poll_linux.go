package main

// epoll, called with raw system calls: the event loop never enters the Go scheduler's syscall path,
// so no other thread is woken to stand in while it sleeps in epoll_wait.

import (
	"syscall"
	"unsafe"
)

type poller struct {
	epfd int
	evs  [128]syscall.EpollEvent
}

func newPoller() (*poller, error) {
	fd, err := syscall.EpollCreate1(syscall.EPOLL_CLOEXEC)
	return &poller{epfd: fd}, err
}

func (p *poller) add(fd int) error {
	ev := syscall.EpollEvent{Events: syscall.EPOLLIN | syscall.EPOLLRDHUP, Fd: int32(fd)}
	return syscall.EpollCtl(p.epfd, syscall.EPOLL_CTL_ADD, fd, &ev)
}

// want switches a connection between waiting to read and waiting to flush its output.
func (p *poller) want(fd int, write bool) {
	ev := syscall.EpollEvent{Events: syscall.EPOLLIN | syscall.EPOLLRDHUP, Fd: int32(fd)}
	if write {
		ev.Events = syscall.EPOLLOUT
	}
	syscall.EpollCtl(p.epfd, syscall.EPOLL_CTL_MOD, fd, &ev)
}

func (p *poller) del(fd int) {
	syscall.EpollCtl(p.epfd, syscall.EPOLL_CTL_DEL, fd, nil)
}

// wait blocks until some descriptor is ready and calls f for each.
func (p *poller) wait(f func(fd int, readable, writable bool)) {
	// epoll_pwait with no signal mask: the same call on amd64 and arm64
	n, _, e := syscall.RawSyscall6(syscall.SYS_EPOLL_PWAIT, uintptr(p.epfd), uintptr(unsafe.Pointer(&p.evs[0])), uintptr(len(p.evs)), ^uintptr(0), 0, 0)
	if e != 0 {
		return // EINTR from a preemption signal: just wait again
	}
	for i := 0; i < int(n); i++ {
		ev := p.evs[i]
		bad := ev.Events&(syscall.EPOLLERR|syscall.EPOLLHUP|syscall.EPOLLRDHUP) != 0
		f(int(ev.Fd), ev.Events&syscall.EPOLLIN != 0 || bad, ev.Events&syscall.EPOLLOUT != 0)
	}
}

func rawRead(fd int, b []byte) (int, syscall.Errno) {
	n, _, e := syscall.RawSyscall(syscall.SYS_READ, uintptr(fd), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return int(n), e
}

func rawWrite(fd int, b []byte) (int, syscall.Errno) {
	n, _, e := syscall.RawSyscall(syscall.SYS_WRITE, uintptr(fd), uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)))
	return int(n), e
}

func rawAccept(lfd int) (int, syscall.Errno) {
	fd, _, e := syscall.RawSyscall6(syscall.SYS_ACCEPT4, uintptr(lfd), 0, 0, syscall.SOCK_NONBLOCK|syscall.SOCK_CLOEXEC, 0, 0)
	return int(fd), e
}
