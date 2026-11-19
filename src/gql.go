package main

// GitHub GraphQL v4 at /api/graphql: a small parser, a schema table that drives both validation and
// introspection, and resolvers over the same store REST serves. It covers the documents gh and
// github-mcp-server send: viewer, repository reads, issue and pull request connections, node(id:),
// __type probes, the issue/comment/pull-request mutations, and batched operations.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ---------------------------------------------------------------- schema

type gType struct {
	name     string
	kind     string // OBJECT, ENUM, INTERFACE, INPUT_OBJECT, SCALAR
	fields   []string
	ftype    map[string]string
	enums    []string
	possible []string
}

var schema = map[string]*gType{}

const schemaSrc = `
object Query viewer:User! repository:Repository node:Node nodes:[Node]! user:User
object Mutation createIssue:CreateIssuePayload closeIssue:CloseIssuePayload reopenIssue:ReopenIssuePayload updateIssue:UpdateIssuePayload addComment:AddCommentPayload createPullRequest:CreatePullRequestPayload mergePullRequest:MergePullRequestPayload closePullRequest:ClosePullRequestPayload
object Repository id:ID! databaseId:Int name:String! nameWithOwner:String! owner:RepositoryOwner! description:String url:URI! isPrivate:Boolean! visibility:RepositoryVisibility! defaultBranchRef:Ref viewerPermission:RepositoryPermission hasIssuesEnabled:Boolean! hasWikiEnabled:Boolean! isFork:Boolean! isArchived:Boolean! stargazerCount:Int! forkCount:Int! createdAt:DateTime! updatedAt:DateTime! pushedAt:DateTime issue:Issue issues:IssueConnection! pullRequest:PullRequest pullRequests:PullRequestConnection! labels:LabelConnection ref:Ref viewerHasStarred:Boolean!
object Issue id:ID! databaseId:Int number:Int! title:String! body:String! bodyText:String! state:IssueState! stateReason:IssueStateReason closed:Boolean! closedAt:DateTime createdAt:DateTime! updatedAt:DateTime! url:URI! author:Actor labels:LabelConnection comments:IssueCommentConnection! assignees:UserConnection! repository:Repository! viewerDidAuthor:Boolean! locked:Boolean!
object PullRequest id:ID! databaseId:Int number:Int! title:String! body:String! state:PullRequestState! merged:Boolean! mergedAt:DateTime closed:Boolean! closedAt:DateTime isDraft:Boolean! headRefName:String! baseRefName:String! headRefOid:GitObjectID! baseRefOid:GitObjectID! mergeable:MergeableState! url:URI! author:Actor createdAt:DateTime! updatedAt:DateTime! files:PullRequestChangedFileConnection additions:Int! deletions:Int! changedFiles:Int! repository:Repository! headRepository:Repository baseRepository:Repository comments:IssueCommentConnection! labels:LabelConnection mergeCommit:Commit locked:Boolean!
object PullRequestChangedFile path:String! additions:Int! deletions:Int! changeType:PatchStatus!
object IssueComment id:ID! databaseId:Int body:String! author:Actor createdAt:DateTime! updatedAt:DateTime! url:URI!
object User id:ID! databaseId:Int login:String! name:String email:String! url:URI!
object Label id:ID! name:String! color:String! description:String
object Ref id:ID! name:String! prefix:String! target:GitObject
object Commit id:ID! oid:GitObjectID! message:String! messageHeadline:String! committedDate:DateTime!
object PageInfo hasNextPage:Boolean! hasPreviousPage:Boolean! startCursor:String endCursor:String
object IssueConnection nodes:[Issue] edges:[IssueEdge] pageInfo:PageInfo! totalCount:Int!
object IssueEdge node:Issue cursor:String!
object PullRequestConnection nodes:[PullRequest] edges:[PullRequestEdge] pageInfo:PageInfo! totalCount:Int!
object PullRequestEdge node:PullRequest cursor:String!
object PullRequestChangedFileConnection nodes:[PullRequestChangedFile] edges:[PullRequestChangedFileEdge] pageInfo:PageInfo! totalCount:Int!
object PullRequestChangedFileEdge node:PullRequestChangedFile cursor:String!
object IssueCommentConnection nodes:[IssueComment] edges:[IssueCommentEdge] pageInfo:PageInfo! totalCount:Int!
object IssueCommentEdge node:IssueComment cursor:String!
object LabelConnection nodes:[Label] edges:[LabelEdge] pageInfo:PageInfo! totalCount:Int!
object LabelEdge node:Label cursor:String!
object UserConnection nodes:[User] edges:[UserEdge] pageInfo:PageInfo! totalCount:Int!
object UserEdge node:User cursor:String!
object CreateIssuePayload clientMutationId:String issue:Issue
object CloseIssuePayload clientMutationId:String issue:Issue
object ReopenIssuePayload clientMutationId:String issue:Issue
object UpdateIssuePayload clientMutationId:String issue:Issue
object AddCommentPayload clientMutationId:String commentEdge:IssueCommentEdge subject:Node timelineEdge:IssueCommentEdge
object CreatePullRequestPayload clientMutationId:String pullRequest:PullRequest
object MergePullRequestPayload clientMutationId:String pullRequest:PullRequest
object ClosePullRequestPayload clientMutationId:String pullRequest:PullRequest
interface Node id:ID! | Repository Issue PullRequest IssueComment User Label Ref Commit
interface Actor login:String! url:URI! | User
interface RepositoryOwner id:ID! login:String! url:URI! | User
interface GitObject id:ID! oid:GitObjectID! | Commit
enum IssueState OPEN CLOSED
enum IssueStateReason COMPLETED NOT_PLANNED REOPENED DUPLICATE
enum PullRequestState OPEN CLOSED MERGED
enum MergeableState MERGEABLE CONFLICTING UNKNOWN
enum PullRequestMergeMethod MERGE SQUASH REBASE
enum RepositoryPermission ADMIN MAINTAIN WRITE TRIAGE READ
enum RepositoryVisibility PRIVATE PUBLIC INTERNAL
enum SearchType ISSUE ISSUE_ADVANCED REPOSITORY USER DISCUSSION
enum IssueOrderField CREATED_AT UPDATED_AT COMMENTS
enum PullRequestOrderField CREATED_AT UPDATED_AT
enum OrderDirection ASC DESC
enum PatchStatus ADDED DELETED RENAMED COPIED MODIFIED CHANGED
input CreateIssueInput repositoryId:ID! title:String! body:String labelIds:[ID!] assigneeIds:[ID!] clientMutationId:String
input CloseIssueInput issueId:ID! stateReason:IssueClosedStateReason clientMutationId:String
input AddCommentInput subjectId:ID! body:String! clientMutationId:String
input CreatePullRequestInput repositoryId:ID! baseRefName:String! headRefName:String! title:String! body:String draft:Boolean clientMutationId:String
input MergePullRequestInput pullRequestId:ID! mergeMethod:PullRequestMergeMethod commitHeadline:String commitBody:String clientMutationId:String
input IssueOrder field:IssueOrderField! direction:OrderDirection!
`

func init() {
	for _, line := range strings.Split(schemaSrc, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		t := &gType{name: f[1], ftype: map[string]string{}}
		switch f[0] {
		case "object":
			t.kind = "OBJECT"
		case "interface":
			t.kind = "INTERFACE"
		case "enum":
			t.kind = "ENUM"
			t.enums = f[2:]
		case "input":
			t.kind = "INPUT_OBJECT"
		}
		if t.kind != "ENUM" {
			rest := f[2:]
			for i, x := range rest {
				if x == "|" {
					t.possible = rest[i+1:]
					rest = rest[:i]
					break
				}
			}
			for _, fd := range rest {
				colon := strings.IndexByte(fd, ':')
				t.fields = append(t.fields, fd[:colon])
				t.ftype[fd[:colon]] = fd[colon+1:]
			}
		}
		schema[t.name] = t
	}
	for _, s := range []string{"ID", "String", "Int", "Float", "Boolean", "DateTime", "URI", "GitObjectID", "IssueClosedStateReason"} {
		schema[s] = &gType{name: s, kind: "SCALAR"}
	}
	for _, s := range []string{"__Type", "__Field", "__EnumValue", "__InputValue", "__Schema"} {
		schema[s] = &gType{name: s, kind: "OBJECT"}
	}
}

func namedType(ref string) string { return strings.Trim(ref, "[]!") }

// ---------------------------------------------------------------- parsing

type gSel struct {
	alias, name string
	args        map[string]any
	sel         []*gSel
	on          string // inline fragment type condition; name == "" for a fragment
	spread      string
	skip        bool
	line, col   int
}

type gOp struct {
	kind, name string
	defaults   map[string]any
	sel        []*gSel
}

type gDoc struct {
	ops   []*gOp
	frags map[string]*gSel
}

type varRef string
type enumVal string

type gParser struct {
	s         string
	i         int
	line, col int
	err       error
}

func (p *gParser) fail(msg string) {
	if p.err == nil {
		p.err = fmt.Errorf("Parse error on %s (line %d, column %d)", msg, p.line, p.col)
	}
}

func (p *gParser) ws() {
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '#':
			for p.i < len(p.s) && p.s[p.i] != '\n' {
				p.i++
			}
		case c == '\n':
			p.i++
			p.line++
			p.col = 1
		case c == ' ' || c == '\t' || c == '\r' || c == ',' || c == 0xEF || c == 0xBB || c == 0xBF:
			p.i++
			p.col++
		default:
			return
		}
	}
}

func (p *gParser) peek() byte {
	p.ws()
	if p.i >= len(p.s) {
		return 0
	}
	return p.s[p.i]
}

func (p *gParser) eat(c byte) bool {
	if p.peek() == c {
		p.i++
		p.col++
		return true
	}
	return false
}

func (p *gParser) expect(c byte) {
	if !p.eat(c) {
		p.fail(fmt.Sprintf("%q", string(c)))
	}
}

func isNameByte(c byte, first bool) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || !first && c >= '0' && c <= '9'
}

func (p *gParser) name() string {
	p.ws()
	st := p.i
	for p.i < len(p.s) && isNameByte(p.s[p.i], p.i == st) {
		p.i++
	}
	p.col += p.i - st
	if st == p.i {
		p.fail("a name")
	}
	return p.s[st:p.i]
}

func parseGQL(src string) (*gDoc, error) {
	p := &gParser{s: src, line: 1, col: 1}
	d := &gDoc{frags: map[string]*gSel{}}
	for p.err == nil && p.peek() != 0 {
		if p.peek() == '{' {
			d.ops = append(d.ops, &gOp{kind: "query", sel: p.selSet()})
			continue
		}
		kw := p.name()
		switch kw {
		case "query", "mutation", "subscription":
			op := &gOp{kind: kw, defaults: map[string]any{}}
			if isNameByte(p.peek(), true) {
				op.name = p.name()
			}
			if p.eat('(') {
				for p.err == nil && !p.eat(')') {
					p.expect('$')
					v := p.name()
					p.expect(':')
					p.typeRef()
					if p.eat('=') {
						op.defaults[v] = p.value(true)
					}
					p.directives()
				}
			}
			p.directives()
			op.sel = p.selSet()
			d.ops = append(d.ops, op)
		case "fragment":
			name := p.name()
			if p.name() != "on" {
				p.fail("on")
			}
			f := &gSel{on: p.name()}
			p.directives()
			f.sel = p.selSet()
			d.frags[name] = f
		default:
			p.fail(kw)
		}
	}
	if p.err == nil && len(d.ops) == 0 {
		p.fail("end of file")
	}
	return d, p.err
}

func (p *gParser) typeRef() {
	if p.eat('[') {
		p.typeRef()
		p.expect(']')
	} else {
		p.name()
	}
	p.eat('!')
}

func (p *gParser) directives() bool {
	skip := false
	for p.err == nil && p.eat('@') {
		d := p.name()
		args := map[string]any{}
		if p.eat('(') {
			args = p.args()
		}
		if v, ok := args["if"]; ok {
			if d == "skip" {
				skip = skip || v == true
			}
			if d == "include" {
				skip = skip || v == false
			}
			if vr, ok := v.(varRef); ok {
				// resolved later: keep the reference
				args["if"] = vr
			}
		}
	}
	return skip
}

func (p *gParser) args() map[string]any {
	out := map[string]any{}
	for p.err == nil && !p.eat(')') {
		k := p.name()
		p.expect(':')
		out[k] = p.value(false)
	}
	return out
}

func (p *gParser) selSet() []*gSel {
	var out []*gSel
	p.expect('{')
	for p.err == nil && !p.eat('}') {
		line, col := p.line, p.col
		if p.peek() == '.' {
			if !strings.HasPrefix(p.s[p.i:], "...") {
				p.fail("...")
				return out
			}
			p.i += 3
			p.col += 3
			if p.peek() == '{' || p.peek() == '@' {
				s := &gSel{line: line, col: col}
				s.skip = p.directives()
				s.sel = p.selSet()
				out = append(out, s)
				continue
			}
			n := p.name()
			if n == "on" {
				s := &gSel{on: p.name(), line: line, col: col}
				s.skip = p.directives()
				s.sel = p.selSet()
				out = append(out, s)
			} else {
				s := &gSel{spread: n, line: line, col: col}
				s.skip = p.directives()
				out = append(out, s)
			}
			continue
		}
		s := &gSel{line: p.line, col: p.col}
		s.name = p.name()
		if p.eat(':') {
			s.alias = s.name
			s.name = p.name()
		}
		if p.eat('(') {
			s.args = p.args()
		}
		s.skip = p.directives()
		if p.peek() == '{' {
			s.sel = p.selSet()
		}
		out = append(out, s)
	}
	return out
}

func (p *gParser) value(constOnly bool) any {
	c := p.peek()
	switch {
	case c == '$' && !constOnly:
		p.i++
		return varRef(p.name())
	case c == '[':
		p.i++
		list := []any{}
		for p.err == nil && !p.eat(']') {
			list = append(list, p.value(constOnly))
		}
		return list
	case c == '{':
		p.i++
		obj := map[string]any{}
		for p.err == nil && !p.eat('}') {
			k := p.name()
			p.expect(':')
			obj[k] = p.value(constOnly)
		}
		return obj
	case c == '"':
		return p.str()
	case c == '-' || c >= '0' && c <= '9':
		st := p.i
		p.i++
		for p.i < len(p.s) && strings.IndexByte("0123456789.eE+-", p.s[p.i]) >= 0 {
			p.i++
		}
		lit := p.s[st:p.i]
		if n, err := strconv.ParseInt(lit, 10, 64); err == nil {
			return int(n)
		}
		f, _ := strconv.ParseFloat(lit, 64)
		return f
	case isNameByte(c, true):
		n := p.name()
		switch n {
		case "true":
			return true
		case "false":
			return false
		case "null":
			return nil
		}
		return enumVal(n)
	}
	p.fail("a value")
	return nil
}

func (p *gParser) str() string {
	if strings.HasPrefix(p.s[p.i:], `"""`) {
		end := strings.Index(p.s[p.i+3:], `"""`)
		if end < 0 {
			p.fail("block string")
			return ""
		}
		v := p.s[p.i+3 : p.i+3+end]
		p.i += end + 6
		return strings.TrimSpace(v)
	}
	st := p.i
	p.i++
	for p.i < len(p.s) && p.s[p.i] != '"' {
		if p.s[p.i] == '\\' {
			p.i++
		}
		p.i++
	}
	p.i++
	var v string
	if err := json.Unmarshal([]byte(p.s[st:min(p.i, len(p.s))]), &v); err != nil {
		p.fail("string")
	}
	return v
}

// ---------------------------------------------------------------- execution

type gErr struct {
	msg  string
	path []any
	line int
	col  int
	ext  O
}

type gExec struct {
	c     *conn
	doc   *gDoc
	vars  map[string]any
	errs  []gErr
	opTag string
}

func graphqlHandler(c *conn) {
	if c.r.method != "POST" {
		c.apiErr(404, "Not Found")
		return
	}
	if !authed(c) {
		c.api(401, []byte(`{"message":"This endpoint requires you to be authenticated.","documentation_url":"https://docs.github.com/graphql/guides/forming-calls-with-graphql#authenticating-with-graphql"}`), "")
		return
	}
	body := bytes.TrimSpace(c.r.body)
	type opReq struct {
		Query         string         `json:"query"`
		Variables     map[string]any `json:"variables"`
		OperationName string         `json:"operationName"`
	}
	mu.Lock()
	defer mu.Unlock()
	if len(body) > 0 && body[0] == '[' {
		var reqs []opReq
		if err := json.Unmarshal(body, &reqs); err != nil {
			c.apiErr(400, "Problems parsing JSON")
			return
		}
		out := []byte{'['}
		for i, r := range reqs {
			if i > 0 {
				out = append(out, ',')
			}
			out = append(out, runGQL(c, r.Query, r.Variables, r.OperationName)...)
		}
		c.api(200, append(out, ']'), "")
		return
	}
	var r opReq
	if err := json.Unmarshal(body, &r); err != nil {
		c.apiErr(400, "Problems parsing JSON")
		return
	}
	c.api(200, runGQL(c, r.Query, r.Variables, r.OperationName), "")
}

func runGQL(c *conn, src string, vars map[string]any, opName string) []byte {
	doc, err := parseGQL(src)
	if err != nil {
		return enc(nil, O{{"errors", []any{O{{"message", err.Error()}}}}})
	}
	var op *gOp
	for _, o := range doc.ops {
		if opName == "" || o.name == opName {
			op = o
			break
		}
	}
	if op == nil {
		return enc(nil, O{{"errors", []any{O{{"message", "Unknown operation named \"" + opName + "\"."}}}}})
	}
	if vars == nil {
		vars = map[string]any{}
	}
	for k, v := range op.defaults {
		if _, ok := vars[k]; !ok {
			vars[k] = v
		}
	}
	x := &gExec{c: c, doc: doc, vars: vars, opTag: op.kind}
	if op.name != "" {
		x.opTag = op.kind + " " + op.name
	}
	root := "Query"
	if op.kind == "mutation" {
		root = "Mutation"
	}
	x.validate(root, op.sel, []any{x.opTag})
	if len(x.errs) > 0 {
		return enc(nil, O{{"errors", x.errList()}})
	}
	data := x.selection(root, nil, op.sel, nil)
	out := O{{"data", data}}
	if len(x.errs) > 0 {
		out = O{{"data", data}, {"errors", x.errList()}}
	}
	return enc(nil, out)
}

func (x *gExec) errList() []any {
	var out []any
	for _, e := range x.errs {
		o := O{{"message", e.msg}}
		if e.line > 0 {
			o = append(o, KV{"locations", []any{O{{"line", e.line}, {"column", e.col}}}})
		}
		if e.path != nil {
			o = append(o, KV{"path", e.path})
		}
		if e.ext != nil {
			o = append(o, KV{"extensions", e.ext})
		}
		out = append(out, o)
	}
	return out
}

// validate reports every field the schema does not hold, as GitHub does before running anything.
func (x *gExec) validate(t string, sels []*gSel, path []any) {
	typ := schema[t]
	for _, s := range sels {
		switch {
		case s.spread != "":
			if f := x.doc.frags[s.spread]; f != nil {
				x.validate(f.on, f.sel, path)
			} else {
				x.errs = append(x.errs, gErr{msg: "Fragment " + s.spread + " was used, but not defined", line: s.line, col: s.col})
			}
			continue
		case s.name == "":
			on := s.on
			if on == "" {
				on = t
			}
			if schema[on] == nil {
				x.errs = append(x.errs, gErr{msg: "No such type " + on + ", so it can't be a fragment condition", line: s.line, col: s.col})
				continue
			}
			x.validate(on, s.sel, path)
			continue
		case s.name == "__typename":
			continue
		case t == "Query" && (s.name == "__type" || s.name == "__schema"):
			continue
		case strings.HasPrefix(t, "__"):
			continue
		}
		ft, ok := "", false
		if typ != nil {
			ft, ok = typ.ftype[s.name]
		}
		if !ok {
			x.errs = append(x.errs, gErr{msg: "Field '" + s.name + "' doesn't exist on type '" + t + "'",
				path: append(append([]any{}, path...), s.name), line: s.line, col: s.col,
				ext: O{{"code", "undefinedField"}, {"typeName", t}, {"fieldName", s.name}}})
			continue
		}
		if len(s.sel) > 0 {
			x.validate(namedType(ft), s.sel, append(append([]any{}, path...), s.name))
		}
	}
}

func (x *gExec) arg(s *gSel, k string) any {
	v, ok := s.args[k]
	if !ok {
		return nil
	}
	return x.resolveValue(v)
}

func (x *gExec) resolveValue(v any) any {
	switch t := v.(type) {
	case varRef:
		return x.vars[string(t)]
	case enumVal:
		return string(t)
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = x.resolveValue(e)
		}
		return out
	case map[string]any:
		out := map[string]any{}
		for k, e := range t {
			out[k] = x.resolveValue(e)
		}
		return out
	}
	return v
}

func argInt(v any, def int) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	case string:
		if i, err := strconv.Atoi(n); err == nil {
			return i
		}
	}
	return def
}

func argStr(v any) string {
	s, _ := v.(string)
	return s
}

// typeOf names the concrete GraphQL type of a resolved value.
func typeOf(v any) string {
	switch t := v.(type) {
	case *Repo:
		return "Repository"
	case *Issue:
		return "Issue"
	case *Pull:
		return "PullRequest"
	case *Comment:
		return "IssueComment"
	case User:
		return "User"
	case *Label:
		return "Label"
	case gRef:
		return "Ref"
	case gCommit:
		return "Commit"
	case *gConn:
		return t.typ
	case gEdge:
		return t.typ
	case gFile:
		return "PullRequestChangedFile"
	case gPayload:
		return t.typ
	case gPageInfo:
		return "PageInfo"
	}
	return ""
}

func (x *gExec) selection(t string, obj any, sels []*gSel, path []any) O {
	out := O{}
	x.collect(t, obj, sels, path, &out)
	return out
}

func (x *gExec) collect(t string, obj any, sels []*gSel, path []any, out *O) {
	for _, s := range sels {
		if s.skip {
			continue
		}
		if s.spread != "" {
			f := x.doc.frags[s.spread]
			if f != nil && (f.on == t || implements(t, f.on)) {
				x.collect(t, obj, f.sel, path, out)
			}
			continue
		}
		if s.name == "" {
			if s.on == "" || s.on == t || implements(t, s.on) {
				x.collect(t, obj, s.sel, path, out)
			}
			continue
		}
		key := s.name
		if s.alias != "" {
			key = s.alias
		}
		dup := false
		for _, kv := range *out {
			if kv.K == key {
				dup = true
			}
		}
		if dup {
			continue
		}
		fpath := append(append([]any{}, path...), key)
		v := x.field(t, obj, s, fpath)
		*out = append(*out, KV{key, v})
	}
}

func implements(t, iface string) bool {
	it := schema[iface]
	if it == nil {
		return false
	}
	for _, p := range it.possible {
		if p == t {
			return true
		}
	}
	return false
}

// field resolves one selection and shapes its value by the sub-selection.
func (x *gExec) field(t string, obj any, s *gSel, path []any) any {
	if s.name == "__typename" {
		return t
	}
	var v any
	var err string
	switch {
	case t == "Query" && s.name == "__type":
		if gt := schema[argStr(x.arg(s, "name"))]; gt != nil {
			v = introType{gt}
		}
	case t == "Query" && s.name == "__schema":
		v = introSchema{}
	case t == "Mutation":
		v, err = x.mutate(s)
	default:
		v, err = x.resolve(t, obj, s)
	}
	if err != "" {
		x.errs = append(x.errs, gErr{msg: err, path: path, line: s.line, col: s.col})
		return nil
	}
	return x.shape(v, s, path)
}

func (x *gExec) shape(v any, s *gSel, path []any) any {
	if v == nil || len(s.sel) == 0 {
		switch val := v.(type) {
		case []string:
			out := make([]any, len(val))
			for i, e := range val {
				out[i] = e
			}
			return out
		}
		return v
	}
	if list, ok := v.([]any); ok {
		out := make([]any, len(list))
		for i, e := range list {
			out[i] = x.shape(e, s, append(append([]any{}, path...), i))
		}
		return out
	}
	switch val := v.(type) {
	case introType, introField, introEnum, introSchema, introRef:
		return x.introSelection(val, s.sel, path)
	}
	return x.selection(typeOf(v), v, s.sel, path)
}

// ---------------------------------------------------------------- resolvers

type gConn struct {
	typ, edge string
	nodes     []any
	start     int
	hasNext   bool
	total     int
}
type gEdge struct {
	typ    string
	node   any
	cursor string
}
type gRef struct {
	repo *Repo
	name string
	id   oid
}
type gCommit struct{ id oid }
type gFile struct{ f changedFile }
type gPayload struct {
	typ    string
	fields map[string]any
}

func cursor(i int) string { return base64.StdEncoding.EncodeToString([]byte("cursor:v2:" + strconv.Itoa(i))) }

func cursorIndex(s string) int {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil || !strings.HasPrefix(string(raw), "cursor:v2:") {
		return -1
	}
	n, err := strconv.Atoi(string(raw[10:]))
	if err != nil {
		return -1
	}
	return n
}

func (x *gExec) connection(typ string, all []any, s *gSel) *gConn {
	start := 0
	if a := argStr(x.arg(s, "after")); a != "" {
		if i := cursorIndex(a); i >= 0 {
			start = i + 1
		}
	}
	if start > len(all) {
		start = len(all)
	}
	end := len(all)
	if f := argInt(x.arg(s, "first"), -1); f >= 0 && start+f < end {
		end = start + f
	}
	if l := argInt(x.arg(s, "last"), -1); l >= 0 && end-l > start {
		start = end - l
	}
	return &gConn{typ: typ + "Connection", edge: typ + "Edge", nodes: all[start:end], start: start, hasNext: end < len(all), total: len(all)}
}

func isoTime(sec int64) string { return time.Unix(sec, 0).UTC().Format("2006-01-02T15:04:05Z") }

func (x *gExec) resolve(t string, obj any, s *gSel) (any, string) {
	n := s.name
	switch t {
	case "Query":
		switch n {
		case "viewer":
			return viewer, ""
		case "user":
			if argStr(x.arg(s, "login")) == viewer.login {
				return viewer, ""
			}
			return nil, ""
		case "repository":
			if argStr(x.arg(s, "owner")) != viewer.login {
				return nil, ""
			}
			if r := repos[viewer.login+"/"+argStr(x.arg(s, "name"))]; r != nil {
				return r, ""
			}
			return nil, ""
		case "node":
			return lookupNode(argStr(x.arg(s, "id"))), ""
		case "nodes":
			var out []any
			if ids, ok := x.arg(s, "ids").([]any); ok {
				for _, id := range ids {
					out = append(out, lookupNode(argStr(id)))
				}
			}
			return out, ""
		}
	case "Repository":
		r := obj.(*Repo)
		switch n {
		case "id":
			return nodeID("R", r.id), ""
		case "databaseId":
			return r.id, ""
		case "name":
			return r.name, ""
		case "nameWithOwner":
			return r.full, ""
		case "owner":
			return viewer, ""
		case "description":
			if r.desc == "" {
				return nil, ""
			}
			return r.desc, ""
		case "url":
			return x.c.baseURL() + "/" + r.full, ""
		case "isPrivate":
			return r.private, ""
		case "visibility":
			if r.private {
				return "PRIVATE", ""
			}
			return "PUBLIC", ""
		case "defaultBranchRef":
			if id, ok := r.branchTip(r.defBranch); ok {
				return gRef{r, "refs/heads/" + r.defBranch, id}, ""
			}
			return nil, ""
		case "ref":
			q := argStr(x.arg(s, "qualifiedName"))
			for _, full := range []string{q, "refs/heads/" + q, "refs/tags/" + q} {
				if id, ok := r.refs[full]; ok {
					return gRef{r, full, id}, ""
				}
			}
			return nil, ""
		case "viewerPermission":
			return "ADMIN", ""
		case "hasIssuesEnabled", "hasWikiEnabled":
			return true, ""
		case "isFork", "isArchived", "viewerHasStarred":
			return false, ""
		case "stargazerCount", "forkCount":
			return 0, ""
		case "createdAt", "updatedAt", "pushedAt":
			return r.created, ""
		case "issue":
			if is := r.issues[argInt(x.arg(s, "number"), 0)]; is != nil {
				return is, ""
			}
			if r.pulls[argInt(x.arg(s, "number"), 0)] != nil {
				return nil, ""
			}
			return nil, "Could not resolve to an issue or pull request with the number of " + strconv.Itoa(argInt(x.arg(s, "number"), 0)) + "."
		case "pullRequest":
			if p := r.pulls[argInt(x.arg(s, "number"), 0)]; p != nil {
				return p, ""
			}
			return nil, "Could not resolve to a PullRequest with the number of " + strconv.Itoa(argInt(x.arg(s, "number"), 0)) + "."
		case "issues":
			return x.issueConn(r, s), ""
		case "pullRequests":
			return x.pullConn(r, s), ""
		case "labels":
			all := make([]any, len(r.labels))
			for i, l := range r.labels {
				all[i] = l
			}
			return x.connection("Label", all, s), ""
		}
	case "Issue":
		is := obj.(*Issue)
		switch n {
		case "id":
			return nodeID("I", is.id), ""
		case "databaseId":
			return is.id, ""
		case "number":
			return is.num, ""
		case "title":
			return is.title, ""
		case "body", "bodyText":
			return is.body, ""
		case "state":
			return strings.ToUpper(is.state), ""
		case "stateReason":
			if is.stateReason == "" {
				return nil, ""
			}
			return strings.ToUpper(is.stateReason), ""
		case "closed":
			return is.state == "closed", ""
		case "closedAt":
			if is.closedAt == "" {
				return nil, ""
			}
			return is.closedAt, ""
		case "createdAt":
			return is.created, ""
		case "updatedAt":
			return is.updated, ""
		case "url":
			return x.c.baseURL() + "/" + is.repo.full + "/issues/" + strconv.Itoa(is.num), ""
		case "author":
			return viewer, ""
		case "labels":
			all := make([]any, len(is.labels))
			for i, l := range is.labels {
				all[i] = l
			}
			return x.connection("Label", all, s), ""
		case "comments":
			all := make([]any, len(is.comments))
			for i, c := range is.comments {
				all[i] = c
			}
			return x.connection("IssueComment", all, s), ""
		case "assignees":
			return x.connection("User", nil, s), ""
		case "repository":
			return is.repo, ""
		case "viewerDidAuthor":
			return true, ""
		case "locked":
			return false, ""
		}
	case "PullRequest":
		p := obj.(*Pull)
		switch n {
		case "id":
			return nodeID("PR", p.id), ""
		case "databaseId":
			return p.id, ""
		case "number":
			return p.num, ""
		case "title":
			return p.title, ""
		case "body":
			return p.body, ""
		case "state":
			if p.merged {
				return "MERGED", ""
			}
			return strings.ToUpper(p.state), ""
		case "merged":
			return p.merged, ""
		case "mergedAt":
			if p.mergedAt == "" {
				return nil, ""
			}
			return p.mergedAt, ""
		case "closed":
			return p.state == "closed", ""
		case "closedAt":
			if p.closedAt == "" {
				return nil, ""
			}
			return p.closedAt, ""
		case "isDraft":
			return p.draft, ""
		case "headRefName":
			return p.head, ""
		case "baseRefName":
			return p.base, ""
		case "headRefOid":
			return hexOID(p.headTip()), ""
		case "baseRefOid":
			return hexOID(p.baseTip()), ""
		case "mergeable":
			if p.state != "open" {
				return "UNKNOWN", ""
			}
			if p.mergeable() {
				return "MERGEABLE", ""
			}
			return "CONFLICTING", ""
		case "url":
			return x.c.baseURL() + "/" + p.repo.full + "/pull/" + strconv.Itoa(p.num), ""
		case "author":
			return viewer, ""
		case "createdAt":
			return p.created, ""
		case "updatedAt":
			return p.updated, ""
		case "files":
			fs := p.files()
			all := make([]any, len(fs))
			for i, f := range fs {
				all[i] = gFile{f}
			}
			return x.connection("PullRequestChangedFile", all, s), ""
		case "additions", "deletions", "changedFiles":
			a, d, fs := 0, 0, p.files()
			for _, f := range fs {
				a += f.additions
				d += f.deletions
			}
			return map[string]int{"additions": a, "deletions": d, "changedFiles": len(fs)}[n], ""
		case "repository", "headRepository", "baseRepository":
			return p.repo, ""
		case "comments":
			all := make([]any, len(p.comments))
			for i, c := range p.comments {
				all[i] = c
			}
			return x.connection("IssueComment", all, s), ""
		case "labels":
			return x.connection("Label", nil, s), ""
		case "mergeCommit":
			if p.merged {
				return gCommit{p.mergeSha}, ""
			}
			return nil, ""
		case "locked":
			return false, ""
		}
	case "PullRequestChangedFile":
		f := obj.(gFile).f
		switch n {
		case "path":
			return f.path, ""
		case "additions":
			return f.additions, ""
		case "deletions":
			return f.deletions, ""
		case "changeType":
			return map[string]string{"added": "ADDED", "removed": "DELETED", "modified": "MODIFIED"}[f.status], ""
		}
	case "IssueComment":
		cm := obj.(*Comment)
		switch n {
		case "id":
			return nodeID("IC", cm.id), ""
		case "databaseId":
			return cm.id, ""
		case "body":
			return cm.body, ""
		case "author":
			return viewer, ""
		case "createdAt", "updatedAt":
			return cm.created, ""
		case "url":
			if cm.issue != nil {
				return x.c.baseURL() + "/" + cm.issue.repo.full + "/issues/" + strconv.Itoa(cm.issue.num) + "#issuecomment-" + strconv.Itoa(cm.id), ""
			}
			return "", ""
		}
	case "User":
		u := obj.(User)
		switch n {
		case "id":
			return nodeID("U", u.id), ""
		case "databaseId":
			return u.id, ""
		case "login":
			return u.login, ""
		case "name":
			return u.name, ""
		case "email":
			return u.email, ""
		case "url":
			return x.c.baseURL() + "/" + u.login, ""
		}
	case "Label":
		l := obj.(*Label)
		switch n {
		case "id":
			return nodeID("LA", l.id), ""
		case "name":
			return l.name, ""
		case "color":
			return l.color, ""
		case "description":
			if l.desc == "" {
				return nil, ""
			}
			return l.desc, ""
		}
	case "Ref":
		ref := obj.(gRef)
		switch n {
		case "id":
			return nodeID("REF", ref.repo.id), ""
		case "name":
			return strings.TrimPrefix(strings.TrimPrefix(ref.name, "refs/heads/"), "refs/tags/"), ""
		case "prefix":
			return ref.name[:strings.LastIndexByte(ref.name, '/')+1], ""
		case "target":
			return gCommit{ref.id}, ""
		}
	case "Commit":
		cid := obj.(gCommit).id
		ci, _ := commitOf(cid)
		switch n {
		case "id":
			return nodeID("C", int(cid[0])<<16|int(cid[1])<<8|int(cid[2])), ""
		case "oid":
			return hexOID(cid), ""
		case "message":
			return strings.TrimRight(ci.message, "\n"), ""
		case "messageHeadline":
			return strings.SplitN(ci.message, "\n", 2)[0], ""
		case "committedDate":
			return identJSON(ci.committer).get("date"), ""
		}
	case "PageInfo":
		cn := obj.(gPageInfo).c
		switch n {
		case "hasNextPage":
			return cn.hasNext, ""
		case "hasPreviousPage":
			return cn.start > 0, ""
		case "startCursor":
			if len(cn.nodes) == 0 {
				return nil, ""
			}
			return cursor(cn.start), ""
		case "endCursor":
			if len(cn.nodes) == 0 {
				return nil, ""
			}
			return cursor(cn.start + len(cn.nodes) - 1), ""
		}
	}
	if cn, ok := obj.(*gConn); ok {
		switch n {
		case "nodes":
			return cn.nodes, ""
		case "edges":
			out := make([]any, len(cn.nodes))
			for i, nd := range cn.nodes {
				out[i] = gEdge{cn.edge, nd, cursor(cn.start + i)}
			}
			return out, ""
		case "pageInfo":
			return gPageInfo{cn}, ""
		case "totalCount":
			return cn.total, ""
		}
	}
	if e, ok := obj.(gEdge); ok {
		if n == "node" {
			return e.node, ""
		}
		return e.cursor, ""
	}
	if p, ok := obj.(gPayload); ok {
		return p.fields[n], ""
	}
	return nil, ""
}

type gPageInfo struct{ c *gConn }

func lookupNode(id string) any {
	kind, n, ok := parseNodeID(id)
	if !ok {
		return nil
	}
	switch kind {
	case "R":
		if r := findRepoByID(n); r != nil {
			return r
		}
	case "I":
		if is := findIssueByID(n); is != nil {
			return is
		}
	case "PR":
		if p := findPullByID(n); p != nil {
			return p
		}
	case "U":
		if n == viewer.id {
			return viewer
		}
	case "IC":
		for _, r := range repoList {
			for _, is := range r.issueList {
				for _, cm := range is.comments {
					if cm.id == n {
						return cm
					}
				}
			}
		}
	case "LA":
		for _, r := range repoList {
			for _, l := range r.labels {
				if l.id == n {
					return l
				}
			}
		}
	}
	return nil
}

func (x *gExec) issueConn(r *Repo, s *gSel) *gConn {
	states := map[string]bool{}
	if st, ok := x.arg(s, "states").([]any); ok {
		for _, v := range st {
			states[strings.ToLower(argStr(v))] = true
		}
	} else if st := argStr(x.arg(s, "states")); st != "" {
		states[strings.ToLower(st)] = true
	}
	list := make([]*Issue, 0, len(r.issueList))
	for _, is := range r.issueList {
		if len(states) == 0 || states[is.state] {
			list = append(list, is)
		}
	}
	if ob, ok := x.arg(s, "orderBy").(map[string]any); ok {
		desc := argStr(ob["direction"]) == "DESC"
		field := argStr(ob["field"])
		sort.SliceStable(list, func(i, j int) bool {
			a, b := list[i], list[j]
			if field == "COMMENTS" && len(a.comments) != len(b.comments) {
				if desc {
					return len(a.comments) > len(b.comments)
				}
				return len(a.comments) < len(b.comments)
			}
			if desc {
				return a.num > b.num
			}
			return a.num < b.num
		})
	}
	all := make([]any, len(list))
	for i, is := range list {
		all[i] = is
	}
	return x.connection("Issue", all, s)
}

func (x *gExec) pullConn(r *Repo, s *gSel) *gConn {
	states := map[string]bool{}
	if st, ok := x.arg(s, "states").([]any); ok {
		for _, v := range st {
			states[argStr(v)] = true
		}
	}
	head, base := argStr(x.arg(s, "headRefName")), argStr(x.arg(s, "baseRefName"))
	var list []*Pull
	for _, p := range r.pullList {
		st := strings.ToUpper(p.state)
		if p.merged {
			st = "MERGED"
		}
		if len(states) > 0 && !states[st] || head != "" && p.head != head || base != "" && p.base != base {
			continue
		}
		list = append(list, p)
	}
	if ob, ok := x.arg(s, "orderBy").(map[string]any); ok && argStr(ob["direction"]) == "DESC" {
		for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
			list[i], list[j] = list[j], list[i]
		}
	}
	all := make([]any, len(list))
	for i, p := range list {
		all[i] = p
	}
	return x.connection("PullRequest", all, s)
}

// ---------------------------------------------------------------- mutations

func (x *gExec) mutate(s *gSel) (any, string) {
	in, _ := x.arg(s, "input").(map[string]any)
	if in == nil {
		return nil, "Argument 'input' on Field '" + s.name + "' is missing or invalid"
	}
	cmid := in["clientMutationId"]
	payload := func(typ string, kv ...any) gPayload {
		f := map[string]any{"clientMutationId": cmid}
		for i := 0; i+1 < len(kv); i += 2 {
			f[kv[i].(string)] = kv[i+1]
		}
		return gPayload{typ, f}
	}
	switch s.name {
	case "createIssue":
		r, _ := lookupNode(argStr(in["repositoryId"])).(*Repo)
		if r == nil {
			return nil, "Could not resolve to a node with the global id of '" + argStr(in["repositoryId"]) + "'."
		}
		var labels []*Label
		if ids, ok := in["labelIds"].([]any); ok {
			for _, id := range ids {
				if l, ok := lookupNode(argStr(id)).(*Label); ok {
					labels = append(labels, l)
				}
			}
		}
		if argStr(in["title"]) == "" {
			return nil, "Title can't be blank"
		}
		return payload("CreateIssuePayload", "issue", newIssue(r, argStr(in["title"]), argStr(in["body"]), labels)), ""
	case "closeIssue", "reopenIssue", "updateIssue":
		idKey := "issueId"
		if s.name == "updateIssue" {
			idKey = "id"
		}
		is, _ := lookupNode(argStr(in[idKey])).(*Issue)
		if is == nil {
			return nil, "Could not resolve to a node with the global id of '" + argStr(in[idKey]) + "'."
		}
		switch s.name {
		case "closeIssue":
			is.setState("closed", strings.ToLower(argStr(in["stateReason"])))
			return payload("CloseIssuePayload", "issue", is), ""
		case "reopenIssue":
			is.setState("open", "reopened")
			return payload("ReopenIssuePayload", "issue", is), ""
		}
		if t, ok := in["title"].(string); ok {
			is.title = t
		}
		if b, ok := in["body"].(string); ok {
			is.body = b
		}
		if st, ok := in["state"].(string); ok {
			is.setState(strings.ToLower(st), "")
		}
		is.js = nil
		return payload("UpdateIssuePayload", "issue", is), ""
	case "addComment":
		subject := lookupNode(argStr(in["subjectId"]))
		var cm *Comment
		switch t := subject.(type) {
		case *Issue:
			cm = addComment(t, nil, argStr(in["body"]))
		case *Pull:
			cm = addComment(nil, t, argStr(in["body"]))
		default:
			return nil, "Could not resolve to a node with the global id of '" + argStr(in["subjectId"]) + "'."
		}
		edge := gEdge{"IssueCommentEdge", cm, cursor(0)}
		return payload("AddCommentPayload", "commentEdge", edge, "timelineEdge", edge, "subject", subject), ""
	case "createPullRequest":
		r, _ := lookupNode(argStr(in["repositoryId"])).(*Repo)
		if r == nil {
			return nil, "Could not resolve to a node with the global id of '" + argStr(in["repositoryId"]) + "'."
		}
		draft, _ := in["draft"].(bool)
		p, why := newPull(r, argStr(in["title"]), argStr(in["body"]), argStr(in["headRefName"]), argStr(in["baseRefName"]), draft)
		if p == nil {
			if why == "exists" {
				return nil, "A pull request already exists for " + r.owner + ":" + argStr(in["headRefName"]) + "."
			}
			return nil, "Head sha can't be blank, Base sha can't be blank, No commits between " + argStr(in["baseRefName"]) + " and " + argStr(in["headRefName"])
		}
		return payload("CreatePullRequestPayload", "pullRequest", p), ""
	case "mergePullRequest", "closePullRequest":
		p, _ := lookupNode(argStr(in["pullRequestId"])).(*Pull)
		if p == nil {
			return nil, "Could not resolve to a node with the global id of '" + argStr(in["pullRequestId"]) + "'."
		}
		if s.name == "closePullRequest" {
			if !p.merged {
				p.state, p.closedAt = "closed", nowStr
			}
			return payload("ClosePullRequestPayload", "pullRequest", p), ""
		}
		method := strings.ToLower(argStr(in["mergeMethod"]))
		if method == "" {
			method = "merge"
		}
		if _, status, msg := p.merge(method, argStr(in["commitHeadline"]), argStr(in["commitBody"])); status != 200 {
			return nil, msg
		}
		return payload("MergePullRequestPayload", "pullRequest", p), ""
	}
	return nil, "Field '" + s.name + "' doesn't exist on type 'Mutation'"
}

// ---------------------------------------------------------------- introspection

type introType struct{ t *gType }
type introField struct {
	name, ref string
}
type introEnum struct{ name string }
type introSchema struct{}
type introRef struct{ ref string }

func (x *gExec) introSelection(v any, sels []*gSel, path []any) O {
	out := O{}
	for _, s := range x.flatten(sels, v) {
		key := s.name
		if s.alias != "" {
			key = s.alias
		}
		out = append(out, KV{key, x.introField(v, s, path)})
	}
	return out
}

func (x *gExec) flatten(sels []*gSel, v any) []*gSel {
	var out []*gSel
	for _, s := range sels {
		switch {
		case s.skip:
		case s.spread != "":
			if f := x.doc.frags[s.spread]; f != nil {
				out = append(out, x.flatten(f.sel, v)...)
			}
		case s.name == "":
			out = append(out, x.flatten(s.sel, v)...)
		default:
			out = append(out, s)
		}
	}
	return out
}

func refType(ref string) introRef { return introRef{ref} }

func (x *gExec) introField(v any, s *gSel, path []any) any {
	wrap := func(val any) any { return x.shape(val, s, path) }
	switch t := v.(type) {
	case introSchema:
		switch s.name {
		case "__typename":
			return "__Schema"
		case "queryType":
			return wrap(introType{schema["Query"]})
		case "mutationType":
			return wrap(introType{schema["Mutation"]})
		case "subscriptionType", "directives":
			if s.name == "directives" {
				return []any{}
			}
			return nil
		case "types":
			names := make([]string, 0, len(schema))
			for n := range schema {
				names = append(names, n)
			}
			sort.Strings(names)
			var list []any
			for _, n := range names {
				list = append(list, introType{schema[n]})
			}
			return wrap(list)
		}
	case introType:
		g := t.t
		switch s.name {
		case "__typename":
			return "__Type"
		case "name":
			return g.name
		case "kind":
			return g.kind
		case "description", "specifiedByURL", "ofType":
			return nil
		case "fields", "inputFields":
			if (s.name == "fields") != (g.kind == "OBJECT" || g.kind == "INTERFACE") {
				return nil
			}
			var list []any
			for _, f := range g.fields {
				list = append(list, introField{f, g.ftype[f]})
			}
			if list == nil {
				list = []any{}
			}
			return wrap(list)
		case "enumValues":
			if g.kind != "ENUM" {
				return nil
			}
			var list []any
			for _, e := range g.enums {
				list = append(list, introEnum{e})
			}
			return wrap(list)
		case "interfaces":
			if g.kind != "OBJECT" {
				return nil
			}
			var list []any
			for _, it := range []string{"Node", "Actor", "RepositoryOwner", "GitObject"} {
				if implements(g.name, it) {
					list = append(list, introType{schema[it]})
				}
			}
			if list == nil {
				list = []any{}
			}
			return wrap(list)
		case "possibleTypes":
			if g.kind != "INTERFACE" {
				return nil
			}
			var list []any
			for _, p := range g.possible {
				list = append(list, introType{schema[p]})
			}
			return wrap(list)
		}
	case introField:
		switch s.name {
		case "__typename":
			if _, ok := v.(introField); ok {
				return "__Field"
			}
		case "name":
			return t.name
		case "description", "deprecationReason", "defaultValue":
			return nil
		case "isDeprecated":
			return false
		case "args":
			return []any{}
		case "type":
			return wrap(refType(t.ref))
		}
	case introEnum:
		switch s.name {
		case "__typename":
			return "__EnumValue"
		case "name":
			return t.name
		case "description", "deprecationReason":
			return nil
		case "isDeprecated":
			return false
		}
	case introRef:
		r := t.ref
		switch {
		case strings.HasSuffix(r, "!"):
			switch s.name {
			case "kind":
				return "NON_NULL"
			case "name":
				return nil
			case "ofType":
				return wrap(refType(r[:len(r)-1]))
			}
		case strings.HasPrefix(r, "["):
			switch s.name {
			case "kind":
				return "LIST"
			case "name":
				return nil
			case "ofType":
				return wrap(refType(r[1 : len(r)-1]))
			}
		default:
			g := schema[r]
			if g == nil {
				g = &gType{name: r, kind: "SCALAR"}
			}
			if s.name == "ofType" {
				return nil
			}
			return x.introField(introType{g}, s, path)
		}
	}
	return nil
}
