package main

// The benchmark's hot requests, driven through route() without a socket: each must answer what
// the suite expects, and the bytes each allocates are reported (go test -run Hot -v).

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"fmt"
	"runtime"
	"strings"
	"testing"
)

func do(c *conn, raw string) []byte {
	if c.parse([]byte(raw)) <= 0 {
		panic("unparsed request: " + raw[:40])
	}
	c.out = c.out[:0]
	route(c)
	return c.out
}

func allocs(f func()) uint64 {
	var a, b runtime.MemStats
	f()
	runtime.ReadMemStats(&a)
	for i := 0; i < 100; i++ {
		f()
	}
	runtime.ReadMemStats(&b)
	return (b.TotalAlloc - a.TotalAlloc) / 100
}

func zpack(objs ...[2]string) []byte {
	b := []byte("PACK\x00\x00\x00\x02\x00\x00\x00")
	b = append(b, byte(len(objs)))
	for _, o := range objs {
		t := map[string]byte{"commit": 1, "tree": 2, "blob": 3}[o[0]]
		size := len(o[1])
		c := t<<4 | byte(size&15)
		size >>= 4
		for size > 0 {
			b = append(b, c|0x80)
			c = byte(size & 0x7f)
			size >>= 7
		}
		b = append(b, c)
		var z bytes.Buffer
		w := zlib.NewWriter(&z)
		w.Write([]byte(o[1]))
		w.Close()
		b = append(b, z.Bytes()...)
	}
	sum := sha1.Sum(b)
	return append(b, sum[:]...)
}

func TestHot(t *testing.T) {
	token = "tok"
	initStore()
	c := &conn{buf: nil, out: make([]byte, 0, 4096)}
	auth := "Authorization: Bearer tok\r\nHost: h\r\n"
	post := func(path, body string) string {
		return fmt.Sprintf("POST %s HTTP/1.1\r\n%sContent-Type: application/json\r\nContent-Length: %d\r\n\r\n%s", path, auth, len(body), body)
	}
	do(c, post("/api/v3/user/repos", `{"name":"hot"}`))
	issueBody := `{"title":"Incorrect GraphQL node ids [bench 1]","body":"A minimal case is attached but the second request fails."}`
	if out := do(c, post("/api/v3/repos/adaiversen/hot/issues", issueBody)); !bytes.HasPrefix(out, []byte("HTTP/1.1 201")) {
		t.Fatalf("issue create: %s", out)
	}
	get := "GET /api/v3/repos/adaiversen/hot/issues/1 HTTP/1.1\r\n" + auth + "\r\n"
	if out := do(c, get); !bytes.HasPrefix(out, []byte("HTTP/1.1 200")) || !bytes.Contains(out, []byte(`"number":1`)) {
		t.Fatalf("issue read: %s", out)
	}
	t.Logf("GET issue     %6d B/op", allocs(func() { do(c, get) }))
	t.Logf("POST issue    %6d B/op", allocs(func() { do(c, post("/api/v3/repos/adaiversen/hot/issues", issueBody)) }))

	// push: a chain of 1 KiB commits, each one receive-pack exchange
	adv := "GET /adaiversen/hot.git/info/refs?service=git-receive-pack HTTP/1.1\r\n" + auth + "\r\n"
	parent := ""
	var pushes []string
	for i := 0; i < 102; i++ {
		blob := strings.Repeat(fmt.Sprintf("line %d of the bench data\n", i), 100)[:1024]
		bid := hashObject(tBlob, []byte(blob))
		tree := "100644 bench-data.txt\x00" + string(bid[:])
		tid := hashObject(tTree, []byte(tree))
		commit := "tree " + hexOID(tid) + "\n"
		if parent != "" {
			commit += "parent " + parent + "\n"
		}
		commit += fmt.Sprintf("author a <a@x> %d +0000\ncommitter a <a@x> %d +0000\n\nbench %d\n", 1700000000+i, 1700000000+i, i)
		cid := hexOID(hashObject(tCommit, []byte(commit)))
		old := parent
		if old == "" {
			old = hexOID(zero)
		}
		cmd := old + " " + cid + " refs/heads/main\x00report-status side-band-64k\n"
		body := fmt.Sprintf("%04x%s0000", len(cmd)+4, cmd) + string(zpack([2]string{"blob", blob}, [2]string{"tree", tree}, [2]string{"commit", commit}))
		pushes = append(pushes, fmt.Sprintf("POST /adaiversen/hot.git/git-receive-pack HTTP/1.1\r\n%sContent-Length: %d\r\n\r\n%s", auth, len(body), body))
		parent = cid
	}
	next := 0
	pushOnce := func() {
		do(c, adv)
		out := do(c, pushes[next])
		next++
		if !bytes.Contains(out, []byte("ok refs/heads/main")) {
			t.Fatalf("push %d: %q", next, out)
		}
	}
	pushOnce()
	t.Logf("push          %6d B/op", allocs(pushOnce))

	// shallow clone: advertisement, ls-refs, fetch with deepen 1
	v2 := "GET /adaiversen/hot.git/info/refs?service=git-upload-pack HTTP/1.1\r\n" + auth + "Git-Protocol: version=2\r\n\r\n"
	pk := func(lines ...string) string {
		var b strings.Builder
		for _, l := range lines {
			if l == "" {
				b.WriteString("0001")
				continue
			}
			fmt.Fprintf(&b, "%04x%s", len(l)+4, l)
		}
		return b.String() + "0000"
	}
	up := func(body string) string {
		return fmt.Sprintf("POST /adaiversen/hot.git/git-upload-pack HTTP/1.1\r\n%sGit-Protocol: version=2\r\nContent-Length: %d\r\n\r\n%s", auth, len(body), body)
	}
	lsrefs := up(pk("command=ls-refs\n", "", "peel\n", "symrefs\n", "unborn\n"))
	fetchReq := up(pk("command=fetch\n", "", "thin-pack\n", "no-progress\n", "ofs-delta\n", "want "+parent+"\n", "deepen 1\n", "done\n"))
	cloneOnce := func() {
		do(c, v2)
		do(c, lsrefs)
		do(c, v2)
		if out := do(c, fetchReq); !bytes.Contains(out, []byte("packfile")) || !bytes.Contains(out, []byte("shallow "+parent)) {
			t.Fatalf("fetch: %q", out)
		}
	}
	cloneOnce()
	t.Logf("clone (4 req) %6d B/op", allocs(cloneOnce))
}
