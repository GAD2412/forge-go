package main

// A minimal HTTP/1.1 server on one OS thread: an epoll (kqueue on a Mac) loop accepts, reads and
// writes with raw system calls, parses requests in place from one growing buffer per connection,
// and answers every batch of requests with a single write. No deadlines, no Date header, no
// goroutine per connection: the suite is the only client.

import (
	"bytes"
	"runtime"
	"strconv"
	"syscall"
)

type req struct {
	method   string
	path     []byte // without the query string
	query    []byte
	auth     []byte
	host     []byte
	gitProto []byte
	body     []byte
	close    bool
}

type conn struct {
	fd      int
	buf     []byte
	out     []byte
	sent    int  // bytes of out already written
	closing bool // close once out is flushed
	waitOut bool // registered for writability while out is pending
	r       req
}

var statusText = map[int]string{
	200: "200 OK", 201: "201 Created", 202: "202 Accepted", 204: "204 No Content",
	400: "400 Bad Request", 401: "401 Unauthorized", 403: "403 Forbidden", 404: "404 Not Found",
	405: "405 Method Not Allowed", 409: "409 Conflict", 422: "422 Unprocessable Entity", 500: "500 Internal Server Error",
}

type server struct {
	p     *poller
	lfd   int
	conns map[int]*conn
}

func serve(lfd int) {
	runtime.LockOSThread()
	p, err := newPoller()
	if err != nil {
		panic(err)
	}
	s := &server{p: p, lfd: lfd, conns: map[int]*conn{}}
	p.add(lfd)
	for {
		p.wait(s.event)
	}
}

func (s *server) event(fd int, readable, writable bool) {
	if fd == s.lfd {
		for {
			nfd, e := rawAccept(s.lfd)
			if e != 0 {
				return
			}
			syscall.SetsockoptInt(nfd, syscall.IPPROTO_TCP, syscall.TCP_NODELAY, 1)
			s.conns[nfd] = &conn{fd: nfd, buf: make([]byte, 0, 16384), out: make([]byte, 0, 4096)}
			s.p.add(nfd)
		}
	}
	c := s.conns[fd]
	if c == nil {
		return
	}
	if writable {
		s.flush(c)
		return
	}
	if readable {
		s.read(c)
	}
}

func (s *server) close(c *conn) {
	s.p.del(c.fd)
	syscall.Close(c.fd)
	delete(s.conns, c.fd)
}

func (s *server) read(c *conn) {
	if len(c.buf) == cap(c.buf) {
		nb := make([]byte, len(c.buf), cap(c.buf)*2)
		copy(nb, c.buf)
		c.buf = nb
	}
	n, e := rawRead(c.fd, c.buf[len(c.buf):cap(c.buf)])
	if e == syscall.EAGAIN || e == syscall.EINTR {
		return
	}
	if e != 0 || n <= 0 {
		s.close(c)
		return
	}
	c.buf = c.buf[:len(c.buf)+n]
	start := 0
	c.out = c.out[:0]
	c.sent = 0
	for !c.closing {
		m := c.parse(c.buf[start:])
		if m < 0 {
			c.out = append(c.out, "HTTP/1.1 400 Bad Request\r\nContent-Length: 0\r\nConnection: close\r\n\r\n"...)
			c.closing = true
			break
		}
		if m == 0 {
			break
		}
		route(c)
		start += m
		if c.r.close {
			c.closing = true
		}
	}
	if start == len(c.buf) {
		c.buf = c.buf[:0]
	} else if start > 0 {
		c.buf = c.buf[:copy(c.buf, c.buf[start:])]
	}
	if len(c.out) > 0 {
		s.flush(c)
	}
}

// flush writes what is left of c.out; on a full socket it waits for EPOLLOUT instead of reading.
func (s *server) flush(c *conn) {
	for c.sent < len(c.out) {
		n, e := rawWrite(c.fd, c.out[c.sent:])
		if e == syscall.EAGAIN {
			if !c.waitOut {
				c.waitOut = true
				s.p.want(c.fd, true)
			}
			return
		}
		if e == syscall.EINTR {
			continue
		}
		if e != 0 || n <= 0 {
			s.close(c)
			return
		}
		c.sent += n
	}
	c.out = c.out[:0]
	c.sent = 0
	if cap(c.out) > 1<<20 {
		c.out = make([]byte, 0, 4096)
	}
	if c.closing {
		s.close(c)
		return
	}
	if c.waitOut {
		c.waitOut = false
		s.p.want(c.fd, false)
	}
}

// parse reads one request from b: the bytes it used, 0 when it is incomplete, -1 when malformed.
func (c *conn) parse(b []byte) int {
	end := bytes.Index(b, []byte("\r\n\r\n"))
	if end < 0 {
		if len(b) > 1<<20 {
			return -1
		}
		return 0
	}
	r := &c.r
	*r = req{}
	line := b[:end]
	nl := bytes.IndexByte(line, '\n')
	first := line
	if nl >= 0 {
		first = line[:nl]
	}
	first = bytes.TrimRight(first, "\r")
	sp1 := bytes.IndexByte(first, ' ')
	sp2 := bytes.LastIndexByte(first, ' ')
	if sp1 <= 0 || sp2 <= sp1 {
		return -1
	}
	switch string(first[:sp1]) {
	case "GET":
		r.method = "GET"
	case "POST":
		r.method = "POST"
	case "PUT":
		r.method = "PUT"
	case "PATCH":
		r.method = "PATCH"
	case "DELETE":
		r.method = "DELETE"
	default:
		r.method = string(first[:sp1])
	}
	target := first[sp1+1 : sp2]
	if q := bytes.IndexByte(target, '?'); q >= 0 {
		r.path, r.query = target[:q], target[q+1:]
	} else {
		r.path = target
	}
	if bytes.HasSuffix(first, []byte("HTTP/1.0")) {
		r.close = true
	}
	clen, chunked := 0, false
	for nl >= 0 {
		line = line[nl+1:]
		nl = bytes.IndexByte(line, '\n')
		h := line
		if nl >= 0 {
			h = line[:nl]
		}
		h = bytes.TrimRight(h, "\r")
		colon := bytes.IndexByte(h, ':')
		if colon <= 0 {
			continue
		}
		name, val := h[:colon], bytes.TrimSpace(h[colon+1:])
		switch len(name) {
		case 4:
			if bytes.EqualFold(name, []byte("host")) {
				r.host = val
			}
		case 10:
			if bytes.EqualFold(name, []byte("connection")) && bytes.EqualFold(val, []byte("close")) {
				r.close = true
			}
		case 12:
			if bytes.EqualFold(name, []byte("git-protocol")) {
				r.gitProto = val
			}
		case 13:
			if bytes.EqualFold(name, []byte("authorization")) {
				r.auth = val
			}
		case 14:
			if bytes.EqualFold(name, []byte("content-length")) {
				n, err := strconv.Atoi(string(val))
				if err != nil || n < 0 {
					return -1
				}
				clen = n
			}
		case 17:
			if bytes.EqualFold(name, []byte("transfer-encoding")) && bytes.Contains(bytes.ToLower(val), []byte("chunked")) {
				chunked = true
			}
		}
	}
	head := end + 4
	if chunked {
		body, used := dechunk(b[head:])
		if used == 0 {
			return 0
		}
		if used < 0 {
			return -1
		}
		r.body = body
		return head + used
	}
	if len(b)-head < clen {
		return 0
	}
	r.body = b[head : head+clen]
	return head + clen
}

// dechunk decodes a chunked body in place: the body, and the bytes used (0 incomplete, -1 malformed).
func dechunk(b []byte) ([]byte, int) {
	out := b[:0:0]
	i := 0
	for {
		nl := bytes.IndexByte(b[i:], '\n')
		if nl < 0 {
			return nil, 0
		}
		sizeLine := bytes.TrimSpace(b[i : i+nl])
		if semi := bytes.IndexByte(sizeLine, ';'); semi >= 0 {
			sizeLine = sizeLine[:semi]
		}
		n, err := strconv.ParseInt(string(sizeLine), 16, 64)
		if err != nil || n < 0 {
			return nil, -1
		}
		i += nl + 1
		if n == 0 {
			// trailers end with an empty line
			for {
				nl = bytes.IndexByte(b[i:], '\n')
				if nl < 0 {
					return nil, 0
				}
				empty := len(bytes.TrimSpace(b[i:i+nl])) == 0
				i += nl + 1
				if empty {
					return out, i
				}
			}
		}
		if len(b)-i < int(n)+2 {
			return nil, 0
		}
		out = append(out, b[i:i+int(n)]...)
		i += int(n) + 2
	}
}

var reqID uint64

// respond appends one whole response to c.out. extra is preformatted header lines, each ending in \r\n.
func (c *conn) respond(status int, ctype string, extra string, body []byte) {
	o := append(c.out, "HTTP/1.1 "...)
	o = append(o, statusText[status]...)
	if ctype != "" {
		o = append(o, "\r\nContent-Type: "...)
		o = append(o, ctype...)
	}
	o = append(o, "\r\nContent-Length: "...)
	o = strconv.AppendInt(o, int64(len(body)), 10)
	o = append(o, "\r\n"...)
	o = append(o, extra...)
	o = append(o, "\r\n"...)
	c.out = append(o, body...)
}

const jsonType = "application/json; charset=utf-8"

// api answers a REST/GraphQL JSON reply carrying the request id GitHub's API always sends.
func (c *conn) api(status int, body []byte, extra string) {
	reqID++
	var id [40]byte
	h := append(id[:0], "X-GitHub-Request-Id: F0RG:"...)
	h = strconv.AppendUint(h, reqID, 16)
	h = append(h, "\r\n"...)
	c.respond(status, jsonType, string(h)+extra, body)
}

func (c *conn) apiErr(status int, msg string) {
	b := append([]byte(`{"message":`), jstr(msg)...)
	b = append(b, `,"documentation_url":"https://docs.github.com/rest"}`...)
	c.api(status, b, "")
}
