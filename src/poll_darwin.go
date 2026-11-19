package main

// kqueue, so the same event loop runs on a Mac for local testing.

import "syscall"

type poller struct {
	kq  int
	evs [128]syscall.Kevent_t
}

func newPoller() (*poller, error) {
	kq, err := syscall.Kqueue()
	return &poller{kq: kq}, err
}

func (p *poller) change(fd int, filter int16, flags uint16) {
	ch := []syscall.Kevent_t{{Ident: uint64(fd), Filter: filter, Flags: flags}}
	syscall.Kevent(p.kq, ch, nil, nil)
}

func (p *poller) add(fd int) error {
	p.change(fd, syscall.EVFILT_READ, syscall.EV_ADD)
	return nil
}

func (p *poller) want(fd int, write bool) {
	if write {
		p.change(fd, syscall.EVFILT_READ, syscall.EV_DISABLE)
		p.change(fd, syscall.EVFILT_WRITE, syscall.EV_ADD|syscall.EV_ENABLE)
	} else {
		p.change(fd, syscall.EVFILT_WRITE, syscall.EV_DELETE)
		p.change(fd, syscall.EVFILT_READ, syscall.EV_ENABLE)
	}
}

func (p *poller) del(fd int) {}

func (p *poller) wait(f func(fd int, readable, writable bool)) {
	n, err := syscall.Kevent(p.kq, nil, p.evs[:], nil)
	if err != nil {
		return
	}
	for i := 0; i < n; i++ {
		ev := p.evs[i]
		bad := ev.Flags&(syscall.EV_EOF|syscall.EV_ERROR) != 0
		f(int(ev.Ident), ev.Filter == syscall.EVFILT_READ || bad, ev.Filter == syscall.EVFILT_WRITE)
	}
}

func errno(err error) syscall.Errno {
	if err == nil {
		return 0
	}
	if e, ok := err.(syscall.Errno); ok {
		return e
	}
	return syscall.EIO
}

func rawRead(fd int, b []byte) (int, syscall.Errno) {
	n, err := syscall.Read(fd, b)
	return n, errno(err)
}

func rawWrite(fd int, b []byte) (int, syscall.Errno) {
	n, err := syscall.Write(fd, b)
	return n, errno(err)
}

func rawAccept(lfd int) (int, syscall.Errno) {
	fd, _, err := syscall.Accept(lfd)
	if err != nil {
		return -1, errno(err)
	}
	syscall.SetNonblock(fd, true)
	syscall.CloseOnExec(fd)
	return fd, 0
}
