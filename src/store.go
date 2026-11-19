package main

// The forge's state: users, repositories, refs, issues, pull requests, labels and comments, all in
// memory behind one lock. Every time the forge writes comes from MAKEFUN_TRIAL_NOW plus a counter,
// never the wall clock, so two runs of a trial answer the same bytes.

import (
	"encoding/base64"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ponytail: one global lock for the whole store; per-repository locks if concurrent writers ever matter.
var mu sync.Mutex

type User struct {
	id    int
	login string
	name  string
	email string
}

type Label struct {
	id    int
	name  string
	color string
	desc  string
}

type Comment struct {
	id      int
	body    string
	created string
	issue   *Issue
}

type Issue struct {
	id          int
	num         int
	repo        *Repo
	title       string
	body        string
	state       string
	stateReason string
	labels      []*Label
	comments    []*Comment
	created     string
	updated     string
	closedAt    string
	js          []byte // cached REST JSON
}

type Pull struct {
	id       int
	num      int
	repo     *Repo
	title    string
	body     string
	state    string
	head     string
	base     string
	headSha  oid
	baseSha  oid
	merged   bool
	mergedAt string
	mergeSha oid
	created  string
	updated  string
	closedAt string
	draft    bool
	comments []*Comment
}

type Repo struct {
	id         int
	owner      string
	name       string
	full       string
	desc       string
	private    bool
	defBranch  string
	head       string
	created    string
	refs       map[string]oid
	issues     map[int]*Issue
	issueList  []*Issue
	pulls      map[int]*Pull
	pullList   []*Pull
	nextNum    int
	labels     []*Label
	recvAdv    []byte
	fetchCache map[string][]byte
}

var (
	viewer    User
	token     string
	repos     = map[string]*Repo{}
	repoList  []*Repo
	nextRepo  = 5000
	nextObjID = 1000000
	nowStr    string
	nowEpoch  int64
	clock     int64 // seconds added to nowEpoch for each commit the forge writes
)

func initStore() {
	login := os.Getenv("MAKEFUN_FORGE_LOGIN")
	if login == "" {
		login = "adaiversen"
	}
	viewer = User{id: 1000, login: login, name: "ada iversen", email: login + "@example.invalid"}
	token = os.Getenv("MAKEFUN_SHOP_TOKEN")
	t, err := time.Parse(time.RFC3339, os.Getenv("MAKEFUN_TRIAL_NOW"))
	if err != nil {
		t = time.Date(2026, 11, 14, 6, 30, 0, 0, time.UTC)
	}
	nowEpoch = t.Unix()
	nowStr = t.UTC().Format("2006-01-02T15:04:05Z")
}

func newID() int {
	nextObjID++
	return nextObjID
}

func (r *Repo) refsMoved() {
	r.recvAdv = nil
	r.fetchCache = map[string][]byte{}
	if _, ok := r.refs[r.head]; !ok {
		// an empty repository's first branch becomes its default, as GitHub does
		for name := range r.refs {
			if strings.HasPrefix(name, "refs/heads/") && len(r.refs) == 1 {
				r.head = name
				r.defBranch = name[11:]
			}
		}
	}
}

func createRepo(name, desc string, private bool) *Repo {
	nextRepo++
	r := &Repo{
		id: nextRepo, owner: viewer.login, name: name, full: viewer.login + "/" + name,
		desc: desc, private: private, defBranch: "main", head: "refs/heads/main", created: nowStr,
		refs: map[string]oid{}, issues: map[int]*Issue{}, pulls: map[int]*Pull{},
		fetchCache: map[string][]byte{},
	}
	repos[r.full] = r
	repoList = append(repoList, r)
	return r
}

func deleteRepo(r *Repo) {
	delete(repos, r.full)
	for i, x := range repoList {
		if x == r {
			repoList = append(repoList[:i], repoList[i+1:]...)
			break
		}
	}
}

func (r *Repo) label(name string) *Label {
	for _, l := range r.labels {
		if strings.EqualFold(l.name, name) {
			return l
		}
	}
	return nil
}

func (r *Repo) branchTip(branch string) (oid, bool) {
	id, ok := r.refs["refs/heads/"+branch]
	return id, ok
}

// resolve turns a branch, tag, full ref or commit id into a commit id.
func (r *Repo) resolve(ref string) (oid, bool) {
	if ref == "" {
		return r.branchTip(r.defBranch)
	}
	for _, full := range []string{"refs/heads/" + ref, "refs/tags/" + ref, ref} {
		if id, ok := r.refs[full]; ok {
			if p, ok := peel(id); ok {
				return p, true
			}
			return id, true
		}
	}
	if id, ok := parseOID(strings.ToLower(ref)); ok && objects[id] != nil {
		return id, true
	}
	return zero, false
}

// ---------------------------------------------------------------- global ids

var b64 = base64.RawURLEncoding

func nodeID(prefix string, id int) string {
	return prefix + "_" + b64.EncodeToString([]byte("forge:"+prefix+":"+strconv.Itoa(id)))
}

// parseNodeID answers the kind prefix and number of a global id, or ok=false.
func parseNodeID(s string) (string, int, bool) {
	us := strings.IndexByte(s, '_')
	if us <= 0 {
		return "", 0, false
	}
	raw, err := b64.DecodeString(s[us+1:])
	if err != nil {
		return "", 0, false
	}
	parts := strings.Split(string(raw), ":")
	if len(parts) != 3 || parts[0] != "forge" || parts[1] != s[:us] {
		return "", 0, false
	}
	n, err := strconv.Atoi(parts[2])
	return parts[1], n, err == nil
}

func findRepoByID(id int) *Repo {
	for _, r := range repoList {
		if r.id == id {
			return r
		}
	}
	return nil
}

func findIssueByID(id int) *Issue {
	for _, r := range repoList {
		for _, is := range r.issueList {
			if is.id == id {
				return is
			}
		}
	}
	return nil
}

func findPullByID(id int) *Pull {
	for _, r := range repoList {
		for _, p := range r.pullList {
			if p.id == id {
				return p
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------- writes shared by REST, GraphQL and MCP

func newIssue(r *Repo, title, body string, labels []*Label) *Issue {
	r.nextNum++
	is := &Issue{id: newID(), num: r.nextNum, repo: r, title: title, body: body, state: "open",
		labels: labels, created: nowStr, updated: nowStr}
	r.issues[is.num] = is
	r.issueList = append(r.issueList, is)
	return is
}

func (is *Issue) setState(state, reason string) {
	is.state = state
	is.js = nil
	if state == "closed" {
		is.closedAt = nowStr
		if reason == "" {
			reason = "completed"
		}
		is.stateReason = reason
	} else {
		is.closedAt = ""
		is.stateReason = reason
	}
}

func addComment(is *Issue, p *Pull, body string) *Comment {
	cm := &Comment{id: newID(), body: body, created: nowStr, issue: is}
	if is != nil {
		is.comments = append(is.comments, cm)
		is.js = nil
	}
	if p != nil {
		p.comments = append(p.comments, cm)
	}
	return cm
}

// newPull opens a pull request; the error is GitHub's validation message.
func newPull(r *Repo, title, body, head, base string, draft bool) (*Pull, string) {
	if i := strings.IndexByte(head, ':'); i >= 0 {
		head = head[i+1:]
	}
	if base == "" {
		base = r.defBranch
	}
	hs, ok := r.branchTip(head)
	if !ok {
		return nil, "head"
	}
	bs, ok := r.branchTip(base)
	if !ok {
		return nil, "base"
	}
	for _, p := range r.pullList {
		if p.state == "open" && p.head == head && p.base == base {
			return nil, "exists"
		}
	}
	r.nextNum++
	p := &Pull{id: newID(), num: r.nextNum, repo: r, title: title, body: body, state: "open",
		head: head, base: base, headSha: hs, baseSha: bs, created: nowStr, updated: nowStr, draft: draft}
	r.pulls[p.num] = p
	r.pullList = append(r.pullList, p)
	return p, ""
}

func (p *Pull) headTip() oid {
	if id, ok := p.repo.branchTip(p.head); ok && !p.merged {
		return id
	}
	return p.headSha
}

func (p *Pull) baseTip() oid {
	if p.merged {
		return p.baseSha
	}
	if id, ok := p.repo.branchTip(p.base); ok {
		return id
	}
	return p.baseSha
}

type fileEnt struct {
	mode string
	id   oid
}

func flatFiles(tree oid) map[string]fileEnt {
	out := map[string]fileEnt{}
	var walk func(id oid, prefix string)
	walk = func(id oid, prefix string) {
		for _, e := range treeOf(id) {
			if isDir(e.mode) {
				walk(e.id, prefix+e.name+"/")
			} else {
				out[prefix+e.name] = fileEnt{e.mode, e.id}
			}
		}
	}
	if tree != zero {
		walk(tree, "")
	}
	return out
}

func buildTree(files map[string]fileEnt) oid {
	type node struct {
		files map[string]fileEnt
		dirs  map[string]*node
	}
	newNode := func() *node { return &node{map[string]fileEnt{}, map[string]*node{}} }
	root := newNode()
	for path, f := range files {
		segs := splitPath(path)
		n := root
		for _, s := range segs[:len(segs)-1] {
			if n.dirs[s] == nil {
				n.dirs[s] = newNode()
			}
			n = n.dirs[s]
		}
		n.files[segs[len(segs)-1]] = f
	}
	var write func(n *node) oid
	write = func(n *node) oid {
		var es []treeEntry
		for name, f := range n.files {
			es = append(es, treeEntry{f.mode, name, f.id})
		}
		for name, d := range n.dirs {
			es = append(es, treeEntry{"40000", name, write(d)})
		}
		return writeTree(es)
	}
	return write(root)
}

func treeOfCommit(id oid) oid {
	if ci, ok := commitOf(id); ok {
		return ci.tree
	}
	return zero
}

// mergedTree applies the head side's changes since the merge base onto base: the tree, or ok=false on a conflict.
func mergedTree(baseTip, headTip oid) (oid, bool) {
	mb, ok := mergeBase(headTip, baseTip)
	if !ok {
		return zero, false
	}
	if mb == baseTip {
		return treeOfCommit(headTip), true
	}
	m := flatFiles(treeOfCommit(mb))
	h := flatFiles(treeOfCommit(headTip))
	b := flatFiles(treeOfCommit(baseTip))
	out := map[string]fileEnt{}
	for k, v := range b {
		out[k] = v
	}
	paths := map[string]bool{}
	for k := range m {
		paths[k] = true
	}
	for k := range h {
		paths[k] = true
	}
	for p := range paths {
		mv, inM := m[p]
		hv, inH := h[p]
		if inM == inH && mv == hv {
			continue // the head side did not touch it
		}
		bv, inB := b[p]
		if (inB != inM || bv != mv) && (inB != inH || bv != hv) {
			return zero, false // both sides changed it differently
		}
		if inH {
			out[p] = hv
		} else {
			delete(out, p)
		}
	}
	return buildTree(out), true
}

func (p *Pull) mergeable() bool {
	if p.merged || p.state != "open" {
		return false
	}
	_, ok := mergedTree(p.baseTip(), p.headTip())
	return ok
}

// merge lands the pull request on its base branch: the merge commit, or a reason it cannot.
func (p *Pull) merge(method, title, message string) (oid, int, string) {
	if p.merged || p.state != "open" {
		return zero, 405, "Pull Request is not mergeable"
	}
	r := p.repo
	baseTip, ok := r.branchTip(p.base)
	if !ok {
		return zero, 405, "Base branch was deleted"
	}
	headTip := p.headTip()
	tree, ok := mergedTree(baseTip, headTip)
	if !ok {
		return zero, 405, "Pull Request is not mergeable"
	}
	if title == "" {
		title = p.title + " (#" + strconv.Itoa(p.num) + ")"
	}
	msg := title + "\n"
	if message != "" {
		msg += "\n" + message + "\n"
	}
	parents := []oid{baseTip}
	if method == "merge" {
		parents = append(parents, headTip)
	}
	sha := commitNow(tree, parents, msg)
	r.refs["refs/heads/"+p.base] = sha
	r.refsMoved()
	p.merged, p.state = true, "closed"
	p.mergedAt, p.closedAt, p.updated = nowStr, nowStr, nowStr
	p.mergeSha, p.headSha, p.baseSha = sha, headTip, baseTip
	return sha, 200, ""
}

func identity() string { return viewer.name + " <" + viewer.email + ">" }

func commitNow(tree oid, parents []oid, msg string) oid {
	clock++
	return writeCommit(tree, parents, identity(), nowEpoch+clock, msg)
}

// changedFiles lists a pull request's files from the merge base to the head tip.
type changedFile struct {
	path      string
	status    string
	additions int
	deletions int
	sha       oid
}

func diffFiles(from, to oid) []changedFile {
	a := flatFiles(from)
	b := flatFiles(to)
	paths := map[string]bool{}
	for k := range a {
		paths[k] = true
	}
	for k := range b {
		paths[k] = true
	}
	var out []changedFile
	for p := range paths {
		av, inA := a[p]
		bv, inB := b[p]
		switch {
		case inA && inB && av == bv:
			continue
		case !inA:
			out = append(out, changedFile{p, "added", countLines(bv.id), 0, bv.id})
		case !inB:
			out = append(out, changedFile{p, "removed", 0, countLines(av.id), av.id})
		default:
			add, del := lineDiff(av.id, bv.id)
			out = append(out, changedFile{p, "modified", add, del, bv.id})
		}
	}
	sortFiles(out)
	return out
}

func sortFiles(f []changedFile) {
	for i := 1; i < len(f); i++ {
		for j := i; j > 0 && f[j].path < f[j-1].path; j-- {
			f[j], f[j-1] = f[j-1], f[j]
		}
	}
}

func lines(id oid) []string {
	o := objects[id]
	if o == nil || len(o.data) == 0 {
		return nil
	}
	s := strings.TrimSuffix(string(o.data), "\n")
	return strings.Split(s, "\n")
}

func countLines(id oid) int { return len(lines(id)) }

// lineDiff counts added and removed lines as a multiset difference (no alignment).
func lineDiff(a, b oid) (int, int) {
	count := map[string]int{}
	for _, l := range lines(a) {
		count[l]++
	}
	add := 0
	for _, l := range lines(b) {
		if count[l] > 0 {
			count[l]--
		} else {
			add++
		}
	}
	del := 0
	for _, n := range count {
		del += n
	}
	return add, del
}

func (p *Pull) files() []changedFile {
	head := p.headTip()
	base := p.baseTip()
	if p.merged {
		base = p.baseSha
	}
	mb, ok := mergeBase(head, base)
	if !ok {
		return nil
	}
	return diffFiles(treeOfCommit(mb), treeOfCommit(head))
}
