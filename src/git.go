package main

// The git object store and the smart-HTTP protocol: upload-pack v2 (ls-refs, fetch with shallow
// and deepen) and receive-pack v0 (report-status, side-band-64k, delete-refs). Objects live in one
// content-addressed map shared by every repository; each object keeps its zlib bytes so a fetch
// never compresses twice (a pushed object reuses the exact bytes the client sent).

import (
	"bytes"
	"compress/zlib"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"io"
	"sort"
	"strconv"
)

type oid = [20]byte

const (
	tCommit = 1
	tTree   = 2
	tBlob   = 3
	tTag    = 4
)

var typeName = [5]string{"", "commit", "tree", "blob", "tag"}

type object struct {
	t    byte
	data []byte
	z    []byte // zlib stream of data
}

var objects = map[oid]*object{}

var zero oid

func hexOID(id oid) string { return hex.EncodeToString(id[:]) }

func parseOID(s string) (oid, bool) {
	var id oid
	if len(s) != 40 {
		return id, false
	}
	_, err := hex.Decode(id[:], []byte(s))
	return id, err == nil
}

func hashObject(t byte, data []byte) oid {
	h := sha1.New()
	var hdr [32]byte
	b := append(hdr[:0], typeName[t]...)
	b = append(b, ' ')
	b = strconv.AppendInt(b, int64(len(data)), 10)
	b = append(b, 0)
	h.Write(b)
	h.Write(data)
	var id oid
	h.Sum(id[:0])
	return id
}

func putObject(t byte, data []byte) oid {
	id := hashObject(t, data)
	if _, ok := objects[id]; !ok {
		objects[id] = &object{t: t, data: data}
	}
	return id
}

var zw, _ = zlib.NewWriterLevel(nil, zlib.BestSpeed)

func (o *object) zbytes() []byte {
	if o.z == nil {
		var b bytes.Buffer
		zw.Reset(&b)
		zw.Write(o.data)
		zw.Close()
		o.z = b.Bytes()
	}
	return o.z
}

// ---------------------------------------------------------------- commits and trees

type commitInfo struct {
	tree      oid
	parents   []oid
	author    string
	committer string
	message   string
}

func parseCommit(data []byte) commitInfo {
	var c commitInfo
	for len(data) > 0 {
		nl := bytes.IndexByte(data, '\n')
		if nl < 0 {
			break
		}
		line := data[:nl]
		data = data[nl+1:]
		if nl == 0 {
			c.message = string(data)
			break
		}
		sp := bytes.IndexByte(line, ' ')
		if sp < 0 {
			continue
		}
		k, v := string(line[:sp]), string(line[sp+1:])
		switch k {
		case "tree":
			c.tree, _ = parseOID(v)
		case "parent":
			if p, ok := parseOID(v); ok {
				c.parents = append(c.parents, p)
			}
		case "author":
			c.author = v
		case "committer":
			c.committer = v
		}
	}
	return c
}

func commitOf(id oid) (commitInfo, bool) {
	o := objects[id]
	if o == nil || o.t != tCommit {
		return commitInfo{}, false
	}
	return parseCommit(o.data), true
}

type treeEntry struct {
	mode string
	name string
	id   oid
}

func parseTree(data []byte) []treeEntry {
	var es []treeEntry
	for len(data) > 0 {
		sp := bytes.IndexByte(data, ' ')
		nul := bytes.IndexByte(data, 0)
		if sp < 0 || nul < sp || len(data) < nul+21 {
			break
		}
		var e treeEntry
		e.mode = string(data[:sp])
		e.name = string(data[sp+1 : nul])
		copy(e.id[:], data[nul+1:nul+21])
		es = append(es, e)
		data = data[nul+21:]
	}
	return es
}

func treeOf(id oid) []treeEntry {
	o := objects[id]
	if o == nil || o.t != tTree {
		return nil
	}
	return parseTree(o.data)
}

func isDir(mode string) bool { return mode == "40000" || mode == "040000" }

// writeTree stores entries in git's order (a directory sorts as if its name ended in '/').
func writeTree(es []treeEntry) oid {
	sort.Slice(es, func(i, j int) bool {
		a, b := es[i].name, es[j].name
		if isDir(es[i].mode) {
			a += "/"
		}
		if isDir(es[j].mode) {
			b += "/"
		}
		return a < b
	})
	var b []byte
	for _, e := range es {
		b = append(b, e.mode...)
		b = append(b, ' ')
		b = append(b, e.name...)
		b = append(b, 0)
		b = append(b, e.id[:]...)
	}
	return putObject(tTree, b)
}

// lookupPath finds path under a root tree: the entry, or ok=false.
func lookupPath(root oid, path string) (treeEntry, bool) {
	cur := treeEntry{mode: "40000", id: root}
	if path == "" {
		return cur, true
	}
	for _, seg := range splitPath(path) {
		if !isDir(cur.mode) {
			return treeEntry{}, false
		}
		found := false
		for _, e := range treeOf(cur.id) {
			if e.name == seg {
				cur, found = e, true
				break
			}
		}
		if !found {
			return treeEntry{}, false
		}
	}
	return cur, true
}

func splitPath(p string) []string {
	var out []string
	for _, s := range bytes.Split([]byte(p), []byte("/")) {
		if len(s) > 0 {
			out = append(out, string(s))
		}
	}
	return out
}

// setPath returns a new root tree with path set to blob (or removed when blob is zero).
func setPath(root oid, segs []string, blob oid) oid {
	var es []treeEntry
	if root != zero {
		es = treeOf(root)
	}
	name := segs[0]
	idx := -1
	for i, e := range es {
		if e.name == name {
			idx = i
			break
		}
	}
	if len(segs) == 1 {
		if blob == zero {
			if idx >= 0 {
				es = append(es[:idx], es[idx+1:]...)
			}
		} else if idx >= 0 {
			es[idx] = treeEntry{"100644", name, blob}
		} else {
			es = append(es, treeEntry{"100644", name, blob})
		}
	} else {
		var sub oid
		if idx >= 0 && isDir(es[idx].mode) {
			sub = es[idx].id
		}
		nsub := setPath(sub, segs[1:], blob)
		empty := len(treeOf(nsub)) == 0
		switch {
		case idx >= 0 && empty:
			es = append(es[:idx], es[idx+1:]...)
		case idx >= 0:
			es[idx] = treeEntry{"40000", name, nsub}
		case !empty:
			es = append(es, treeEntry{"40000", name, nsub})
		}
	}
	return writeTree(es)
}

func writeCommit(tree oid, parents []oid, ident string, when int64, msg string) oid {
	var b []byte
	b = append(b, "tree "...)
	b = append(b, hexOID(tree)...)
	b = append(b, '\n')
	for _, p := range parents {
		b = append(b, "parent "...)
		b = append(b, hexOID(p)...)
		b = append(b, '\n')
	}
	stamp := ident + " " + strconv.FormatInt(when, 10) + " +0000\n"
	b = append(b, "author "...)
	b = append(b, stamp...)
	b = append(b, "committer "...)
	b = append(b, stamp...)
	b = append(b, '\n')
	b = append(b, msg...)
	return putObject(tCommit, b)
}

// isAncestor reports whether anc is reachable from id through parents.
func isAncestor(anc, id oid) bool {
	seen := map[oid]bool{}
	stack := []oid{id}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if c == anc {
			return true
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		if ci, ok := commitOf(c); ok {
			stack = append(stack, ci.parents...)
		}
	}
	return false
}

func ancestors(id oid) map[oid]bool {
	seen := map[oid]bool{}
	stack := []oid{id}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[c] {
			continue
		}
		seen[c] = true
		if ci, ok := commitOf(c); ok {
			stack = append(stack, ci.parents...)
		}
	}
	return seen
}

// mergeBase is the first ancestor of a (breadth first) that b also reaches.
func mergeBase(a, b oid) (oid, bool) {
	inB := ancestors(b)
	seen := map[oid]bool{}
	queue := []oid{a}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if inB[c] {
			return c, true
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		if ci, ok := commitOf(c); ok {
			queue = append(queue, ci.parents...)
		}
	}
	return zero, false
}

// ---------------------------------------------------------------- packs

var errPack = errors.New("bad pack")

var zr io.ReadCloser

// unpack stores every object of a pack. Delta entries are refused.
// ponytail: no OFS/REF delta resolution; the suite's client always sends whole objects, add it when a real git pushes thin packs.
func unpack(p []byte) error {
	if len(p) < 12 || string(p[:4]) != "PACK" {
		return errPack
	}
	n := int(p[8])<<24 | int(p[9])<<16 | int(p[10])<<8 | int(p[11])
	off := 12
	br := bytes.NewReader(nil)
	for i := 0; i < n; i++ {
		if off >= len(p) {
			return errPack
		}
		c := p[off]
		off++
		t := (c >> 4) & 7
		size := int(c & 15)
		shift := 4
		for c&0x80 != 0 {
			if off >= len(p) {
				return errPack
			}
			c = p[off]
			off++
			size |= int(c&0x7f) << shift
			shift += 7
		}
		if t < tCommit || t > tTag {
			return errors.New("delta objects are not supported")
		}
		br.Reset(p[off:])
		var err error
		if zr == nil {
			zr, err = zlib.NewReader(br)
		} else {
			err = zr.(zlib.Resetter).Reset(br, nil)
		}
		if err != nil {
			return errPack
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(zr, data); err != nil {
			return errPack
		}
		var one [1]byte
		if m, err := zr.Read(one[:]); m != 0 || err != io.EOF {
			return errPack
		}
		used := len(p[off:]) - br.Len()
		id := hashObject(t, data)
		if objects[id] == nil {
			objects[id] = &object{t: t, data: data, z: p[off : off+used : off+used]}
		}
		off += used
	}
	return nil
}

func writePack(ids []oid) []byte {
	b := make([]byte, 12, 64+len(ids)*256)
	copy(b, "PACK")
	b[7] = 2
	b[8], b[9], b[10], b[11] = byte(len(ids)>>24), byte(len(ids)>>16), byte(len(ids)>>8), byte(len(ids))
	for _, id := range ids {
		o := objects[id]
		size := len(o.data)
		c := o.t<<4 | byte(size&15)
		size >>= 4
		for size > 0 {
			b = append(b, c|0x80)
			c = byte(size & 0x7f)
			size >>= 7
		}
		b = append(b, c)
		b = append(b, o.zbytes()...)
	}
	sum := sha1.Sum(b)
	return append(b, sum[:]...)
}

// ---------------------------------------------------------------- pkt-lines

func pkt(b []byte, s string) []byte {
	n := len(s) + 4
	b = append(b, hexDigits[n>>12&15], hexDigits[n>>8&15], hexDigits[n>>4&15], hexDigits[n&15])
	return append(b, s...)
}

func pktBytes(b []byte, s []byte) []byte {
	n := len(s) + 4
	b = append(b, hexDigits[n>>12&15], hexDigits[n>>8&15], hexDigits[n>>4&15], hexDigits[n&15])
	return append(b, s...)
}

// readPkts splits a pkt-line stream: data lines (newline trimmed), "" for delim, nil-marker via flush index.
type pktLine struct {
	kind int // 0 data, 1 flush, 2 delim
	data []byte
}

func readPkts(b []byte) ([]pktLine, int, error) {
	var out []pktLine
	i := 0
	for i+4 <= len(b) {
		n, err := strconv.ParseUint(string(b[i:i+4]), 16, 16)
		if err != nil {
			return out, i, errPack
		}
		switch {
		case n == 0:
			out = append(out, pktLine{kind: 1})
			i += 4
			return out, i, nil
		case n == 1:
			out = append(out, pktLine{kind: 2})
			i += 4
			continue
		case n < 4 || i+int(n) > len(b):
			return out, i, errPack
		}
		out = append(out, pktLine{data: bytes.TrimSuffix(b[i+4:i+int(n)], []byte("\n"))})
		i += int(n)
	}
	return out, i, errPack
}

// sideband frames data on a band in packets of at most 65520 bytes.
func sideband(b []byte, band byte, data []byte) []byte {
	for len(data) > 0 {
		n := len(data)
		if n > 65515 {
			n = 65515
		}
		l := n + 5
		b = append(b, hexDigits[l>>12&15], hexDigits[l>>8&15], hexDigits[l>>4&15], hexDigits[l&15], band)
		b = append(b, data[:n]...)
		data = data[n:]
	}
	return b
}

// ---------------------------------------------------------------- smart HTTP

const agent = "agent=forge-go/1.0"

var v2Advert = func() []byte {
	var b []byte
	b = pkt(b, "version 2\n")
	b = pkt(b, agent+"\n")
	b = pkt(b, "ls-refs=unborn\n")
	b = pkt(b, "fetch=shallow\n")
	b = pkt(b, "server-option\n")
	b = pkt(b, "object-format=sha1\n")
	return append(b, "0000"...)
}()

const (
	ctUploadAdv  = "application/x-git-upload-pack-advertisement"
	ctReceiveAdv = "application/x-git-receive-pack-advertisement"
	ctUploadRes  = "application/x-git-upload-pack-result"
	ctReceiveRes = "application/x-git-receive-pack-result"
	noCache      = "Cache-Control: no-cache\r\n"
)

// sortedRefs lists a repository's refs by name.
func (r *Repo) sortedRefs() []string {
	names := make([]string, 0, len(r.refs))
	for n := range r.refs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func peel(id oid) (oid, bool) {
	o := objects[id]
	if o == nil || o.t != tTag {
		return zero, false
	}
	for o != nil && o.t == tTag {
		nl := bytes.IndexByte(o.data, '\n')
		if nl < 7 {
			return zero, false
		}
		t, ok := parseOID(string(o.data[7:nl]))
		if !ok {
			return zero, false
		}
		id = t
		o = objects[id]
	}
	return id, true
}

// gitInfoRefs answers GET /{owner}/{repo}.git/info/refs?service=...
func gitInfoRefs(c *conn, r *Repo, service string) {
	switch service {
	case "git-upload-pack":
		if bytes.Contains(c.r.gitProto, []byte("version=2")) {
			c.respond(200, ctUploadAdv, noCache, v2Advert)
			return
		}
		var b []byte
		b = pkt(b, "# service=git-upload-pack\n")
		b = append(b, "0000"...)
		caps := "multi_ack_detailed thin-pack side-band side-band-64k ofs-delta shallow no-progress include-tag symref=HEAD:" + r.head + " " + agent + " object-format=sha1"
		first := true
		line := func(id oid, name string) {
			if first {
				b = pkt(b, hexOID(id)+" "+name+"\x00"+caps+"\n")
				first = false
			} else {
				b = pkt(b, hexOID(id)+" "+name+"\n")
			}
		}
		if h, ok := r.refs[r.head]; ok {
			line(h, "HEAD")
		}
		for _, n := range r.sortedRefs() {
			line(r.refs[n], n)
			if p, ok := peel(r.refs[n]); ok {
				line(p, n+"^{}")
			}
		}
		if first {
			line(zero, "capabilities^{}")
		}
		b = append(b, "0000"...)
		c.respond(200, ctUploadAdv, noCache, b)
	case "git-receive-pack":
		if r.recvAdv == nil {
			var b []byte
			b = pkt(b, "# service=git-receive-pack\n")
			b = append(b, "0000"...)
			caps := "report-status side-band-64k delete-refs ofs-delta quiet " + agent + " object-format=sha1"
			names := r.sortedRefs()
			if len(names) == 0 {
				b = pkt(b, hexOID(zero)+" capabilities^{}\x00"+caps+"\n")
			}
			for i, n := range names {
				if i == 0 {
					b = pkt(b, hexOID(r.refs[n])+" "+n+"\x00"+caps+"\n")
				} else {
					b = pkt(b, hexOID(r.refs[n])+" "+n+"\n")
				}
			}
			b = append(b, "0000"...)
			r.recvAdv = b
		}
		c.respond(200, ctReceiveAdv, noCache, r.recvAdv)
	default:
		c.respond(403, "text/plain", "", []byte("unsupported service\n"))
	}
}

// gitUploadPack answers POST git-upload-pack (protocol v2 commands).
func gitUploadPack(c *conn, r *Repo) {
	key := string(c.r.body)
	if b, ok := r.fetchCache[key]; ok {
		c.respond(200, ctUploadRes, noCache, b)
		return
	}
	pk, _, err := readPkts(c.r.body)
	if err != nil {
		c.respond(400, "text/plain", "", []byte("malformed pkt-line request\n"))
		return
	}
	cmd := ""
	args := [][]byte{}
	inArgs := false
	for _, p := range pk {
		switch {
		case p.kind == 2:
			inArgs = true
		case p.kind == 0 && !inArgs && bytes.HasPrefix(p.data, []byte("command=")):
			cmd = string(p.data[8:])
		case p.kind == 0 && inArgs:
			args = append(args, p.data)
		}
	}
	var out []byte
	cacheable := false
	switch cmd {
	case "ls-refs":
		out = lsRefs(r, args)
		cacheable = true // dropped whenever a ref moves
	case "fetch":
		out, cacheable = fetch(r, args)
	default:
		c.respond(400, "text/plain", "", []byte("unknown command\n"))
		return
	}
	if cacheable {
		if len(r.fetchCache) > 512 {
			r.fetchCache = map[string][]byte{}
		}
		r.fetchCache[key] = out
	}
	c.respond(200, ctUploadRes, noCache, out)
}

func lsRefs(r *Repo, args [][]byte) []byte {
	var prefixes []string
	peelOn, symrefs, unborn := false, false, false
	for _, a := range args {
		switch {
		case string(a) == "peel":
			peelOn = true
		case string(a) == "symrefs":
			symrefs = true
		case string(a) == "unborn":
			unborn = true
		case bytes.HasPrefix(a, []byte("ref-prefix ")):
			prefixes = append(prefixes, string(a[11:]))
		}
	}
	match := func(name string) bool {
		if len(prefixes) == 0 {
			return true
		}
		for _, p := range prefixes {
			if len(name) >= len(p) && name[:len(p)] == p {
				return true
			}
		}
		return false
	}
	var b []byte
	if match("HEAD") {
		sym := ""
		if symrefs {
			sym = " symref-target:" + r.head
		}
		if h, ok := r.refs[r.head]; ok {
			b = pkt(b, hexOID(h)+" HEAD"+sym+"\n")
		} else if unborn {
			b = pkt(b, "unborn HEAD"+sym+"\n")
		}
	}
	for _, n := range r.sortedRefs() {
		if !match(n) {
			continue
		}
		line := hexOID(r.refs[n]) + " " + n
		if peelOn {
			if p, ok := peel(r.refs[n]); ok {
				line += " peeled:" + hexOID(p)
			}
		}
		b = pkt(b, line+"\n")
	}
	return append(b, "0000"...)
}

// fetch builds the v2 fetch answer: shallow-info when the depth moves, then the packfile.
func fetch(r *Repo, args [][]byte) ([]byte, bool) {
	var wants, haves []oid
	clientShallow := map[oid]bool{}
	depth := 0
	done := false
	for _, a := range args {
		s := string(a)
		switch {
		case len(s) > 5 && s[:5] == "want ":
			if id, ok := parseOID(s[5:]); ok {
				wants = append(wants, id)
			}
		case len(s) > 5 && s[:5] == "have ":
			if id, ok := parseOID(s[5:]); ok && objects[id] != nil {
				haves = append(haves, id)
			}
		case len(s) > 8 && s[:8] == "shallow ":
			if id, ok := parseOID(s[8:]); ok {
				clientShallow[id] = true
			}
		case len(s) > 7 && s[:7] == "deepen ":
			depth, _ = strconv.Atoi(s[7:])
		case s == "done":
			done = true
		}
	}
	var b []byte
	if !done && len(haves) > 0 {
		b = pkt(b, "acknowledgments\n")
		for _, h := range haves {
			b = pkt(b, "ACK "+hexOID(h)+"\n")
		}
		b = pkt(b, "ready\n")
		b = append(b, "0001"...)
	} else if !done {
		// nothing in common yet and the client did not say done: a NAK ends this round
		if len(wants) > 0 && len(clientShallow) == 0 && depth == 0 {
			// a client that sends no haves is cloning: answer the pack at once
		} else {
			b = pkt(b, "acknowledgments\n")
			b = pkt(b, "NAK\n")
			return append(b, "0000"...), false
		}
	}

	// what the client holds: every object reachable from its haves, stopping at its shallow commits
	have := map[oid]bool{}
	var markTree func(id oid)
	markTree = func(id oid) {
		if have[id] {
			return
		}
		have[id] = true
		for _, e := range treeOf(id) {
			if isDir(e.mode) {
				markTree(e.id)
			} else {
				have[e.id] = true
			}
		}
	}
	stack := append([]oid(nil), haves...)
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if have[id] {
			continue
		}
		o := objects[id]
		if o == nil {
			continue
		}
		if o.t == tTag {
			have[id] = true
			if t, ok := peel(id); ok {
				stack = append(stack, t)
			}
			continue
		}
		if o.t != tCommit {
			continue
		}
		have[id] = true
		ci := parseCommit(o.data)
		markTree(ci.tree)
		if !clientShallow[id] {
			stack = append(stack, ci.parents...)
		}
	}

	var send []oid
	sent := map[oid]bool{}
	add := func(id oid) {
		if !have[id] && !sent[id] {
			sent[id] = true
			send = append(send, id)
		}
	}
	var addTree func(id oid)
	addTree = func(id oid) {
		if have[id] || sent[id] {
			return
		}
		add(id)
		for _, e := range treeOf(id) {
			if isDir(e.mode) {
				addTree(e.id)
			} else if e.mode != "160000" {
				add(e.id)
			}
		}
	}
	var shallowLines []byte
	type item struct {
		id oid
		d  int
	}
	queue := []item{}
	for _, w := range wants {
		id := w
		for {
			o := objects[id]
			if o == nil {
				return errorPack("missing object " + hexOID(id)), false
			}
			if o.t != tTag {
				break
			}
			add(id)
			id, _ = peel(id)
		}
		queue = append(queue, item{id, 1})
	}
	inWindow := map[oid]bool{}
	boundary := map[oid]bool{}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		if inWindow[it.id] {
			continue
		}
		o := objects[it.id]
		if o == nil {
			continue
		}
		if o.t != tCommit {
			if o.t == tTree {
				addTree(it.id)
			} else {
				add(it.id)
			}
			continue
		}
		if depth == 0 && have[it.id] {
			continue
		}
		inWindow[it.id] = true
		ci := parseCommit(o.data)
		if !have[it.id] {
			add(it.id)
			addTree(ci.tree)
		}
		if depth > 0 && it.d >= depth {
			if len(ci.parents) > 0 {
				boundary[it.id] = true
			}
			continue
		}
		for _, p := range ci.parents {
			queue = append(queue, item{p, it.d + 1})
		}
	}
	if depth > 0 {
		// deterministic order: the order commits entered the pack, then the rest
		for _, id := range send {
			if boundary[id] {
				shallowLines = pkt(shallowLines, "shallow "+hexOID(id)+"\n")
			}
		}
		var extra []string
		for id := range boundary {
			if !sent[id] && !clientShallow[id] {
				extra = append(extra, hexOID(id))
			}
		}
		sort.Strings(extra)
		for _, h := range extra {
			shallowLines = pkt(shallowLines, "shallow "+h+"\n")
		}
		var unsh []string
		for id := range clientShallow {
			if inWindow[id] && !boundary[id] {
				unsh = append(unsh, hexOID(id))
			}
		}
		sort.Strings(unsh)
		for _, h := range unsh {
			shallowLines = pkt(shallowLines, "unshallow "+h+"\n")
		}
	}
	if len(shallowLines) > 0 {
		b = pkt(b, "shallow-info\n")
		b = append(b, shallowLines...)
		b = append(b, "0001"...)
	}
	b = pkt(b, "packfile\n")
	b = sideband(b, 1, writePack(send))
	return append(b, "0000"...), true
}

func errorPack(msg string) []byte {
	var b []byte
	b = pkt(b, "packfile\n")
	b = sideband(b, 3, []byte(msg))
	return append(b, "0000"...)
}

// gitReceivePack answers POST git-receive-pack: unpack, then each command as ok or ng.
func gitReceivePack(c *conn, r *Repo) {
	pk, used, err := readPkts(c.r.body)
	if err != nil {
		c.respond(400, "text/plain", "", []byte("malformed command list\n"))
		return
	}
	type command struct {
		old, new oid
		ref      string
	}
	var cmds []command
	sb := false
	for i, p := range pk {
		if p.kind != 0 {
			continue
		}
		line := p.data
		if i == 0 {
			if nul := bytes.IndexByte(line, 0); nul >= 0 {
				caps := line[nul+1:]
				sb = bytes.Contains(caps, []byte("side-band-64k"))
				line = line[:nul]
			}
		}
		f := bytes.Fields(line)
		if len(f) != 3 {
			continue
		}
		o, ok1 := parseOID(string(f[0]))
		n, ok2 := parseOID(string(f[1]))
		if ok1 && ok2 {
			cmds = append(cmds, command{o, n, string(f[2])})
		}
	}
	unpackStatus := "ok"
	if pack := c.r.body[used:]; len(pack) > 0 {
		// the pack bytes are referenced by the stored objects, so keep them out of the read buffer
		if err := unpack(append([]byte(nil), pack...)); err != nil {
			unpackStatus = err.Error()
		}
	}
	var rep []byte
	rep = pkt(rep, "unpack "+unpackStatus+"\n")
	moved := false
	for _, cmd := range cmds {
		cur, exists := r.refs[cmd.ref]
		reason := ""
		switch {
		case unpackStatus != "ok":
			reason = "unpacker error"
		case !(exists && cur == cmd.old) && !(!exists && cmd.old == zero):
			if cmd.new == zero && !exists {
				reason = "no such ref"
			} else if exists && cmd.old == zero {
				reason = "already exists"
			} else {
				reason = "stale info"
			}
		case cmd.new == zero:
			// delete
		case objects[cmd.new] == nil:
			reason = "missing necessary objects"
		case exists && len(cmd.ref) > 11 && cmd.ref[:11] == "refs/heads/" && !isAncestor(cmd.old, cmd.new):
			reason = "non-fast-forward"
		case exists && len(cmd.ref) > 10 && cmd.ref[:10] == "refs/tags/":
			reason = "already exists"
		}
		if reason != "" {
			rep = pkt(rep, "ng "+cmd.ref+" "+reason+"\n")
			continue
		}
		if cmd.new == zero {
			delete(r.refs, cmd.ref)
		} else {
			r.refs[cmd.ref] = cmd.new
		}
		moved = true
		rep = pkt(rep, "ok "+cmd.ref+"\n")
	}
	rep = append(rep, "0000"...)
	if moved {
		r.refsMoved()
	}
	if !sb {
		c.respond(200, ctReceiveRes, noCache, rep)
		return
	}
	out := make([]byte, 0, len(rep)+16)
	out = sideband(out, 1, rep)
	out = append(out, "0000"...)
	c.respond(200, ctReceiveRes, noCache, out)
}
