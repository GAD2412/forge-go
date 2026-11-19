package main

// GitHub REST v3 under /api/v3, the routes the suite and gh drive, in GitHub's shapes and statuses.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// route dispatches one parsed request.
func route(c *conn) {
	p := c.r.path
	switch {
	case bytes.HasPrefix(p, []byte("/api/v3")):
		if !authed(c) {
			c.apiErr(401, "Requires authentication")
			return
		}
		mu.Lock()
		rest(c, p[7:])
		mu.Unlock()
	case bytes.Equal(p, []byte("/api/graphql")) || bytes.Equal(p, []byte("/graphql")):
		graphqlHandler(c)
	case bytes.Equal(p, []byte("/mcp")):
		mcpHandler(c)
	case bytes.Equal(p, []byte("/healthz")):
		c.respond(200, jsonType, "", []byte(`{"ok":true}`))
	case bytes.Contains(p, []byte(".git/")):
		gitRoute(c, p)
	default:
		c.apiErr(404, "Not Found")
	}
}

func authed(c *conn) bool {
	a := c.r.auth
	for _, pre := range []string{"Bearer ", "bearer ", "token ", "Token "} {
		if bytes.HasPrefix(a, []byte(pre)) {
			t := bytes.TrimSpace(a[len(pre):])
			return len(t) > 0 && (token == "" || string(t) == token)
		}
	}
	return false
}

// gitRoute serves /{owner}/{repo}.git/info/refs, /git-upload-pack and /git-receive-pack.
func gitRoute(c *conn, p []byte) {
	i := bytes.Index(p, []byte(".git/"))
	full := strings.TrimPrefix(string(p[:i]), "/")
	action := string(p[i+5:])
	mu.Lock()
	defer mu.Unlock()
	r := repos[full]
	if r == nil || !authed(c) && r.private {
		if r == nil {
			c.respond(404, "text/plain", "", []byte("Repository not found\n"))
		} else {
			c.respond(401, "text/plain", "WWW-Authenticate: Basic realm=\"forge\"\r\n", []byte("Authentication required\n"))
		}
		return
	}
	switch {
	case action == "info/refs" && c.r.method == "GET":
		q, _ := url.ParseQuery(string(c.r.query))
		gitInfoRefs(c, r, q.Get("service"))
	case action == "git-upload-pack" && c.r.method == "POST":
		gitUploadPack(c, r)
	case action == "git-receive-pack" && c.r.method == "POST":
		if !authed(c) {
			c.respond(401, "text/plain", "WWW-Authenticate: Basic realm=\"forge\"\r\n", []byte("Authentication required\n"))
			return
		}
		gitReceivePack(c, r)
	default:
		c.respond(404, "text/plain", "", []byte("Not Found\n"))
	}
}

func (c *conn) baseURL() string { return "http://" + string(c.r.host) }

func (c *conn) obj(status int, v any) {
	c.api(status, enc(make([]byte, 0, 1024), v), "")
}

// rest serves one /api/v3 path (p starts after /api/v3).
func rest(c *conn, p []byte) {
	m := c.r.method
	var seg [12][]byte
	n := 0
	for _, s := range bytes.Split(bytes.Trim(p, "/"), []byte("/")) {
		if n < len(seg) {
			seg[n] = s
		}
		n++
	}
	if len(bytes.Trim(p, "/")) == 0 {
		n = 0
	}
	s := func(i int) string {
		if i >= n || i >= len(seg) {
			return ""
		}
		return string(seg[i])
	}
	switch {
	case n == 0 && m == "GET":
		base := c.baseURL() + "/api/v3"
		c.api(200, enc(nil, O{{"current_user_url", base + "/user"}, {"repository_url", base + "/repos/{owner}/{repo}"},
			{"user_repositories_url", base + "/user/repos"}, {"issues_url", base + "/issues"}}), "X-OAuth-Scopes: repo, read:org, workflow\r\nX-Accepted-OAuth-Scopes: \r\n")
		return
	case n == 1 && s(0) == "meta" && m == "GET":
		c.obj(200, O{{"verifiable_password_authentication", false}, {"installed_version", "3.17.0"}})
		return
	case n == 1 && s(0) == "user" && m == "GET":
		c.api(200, enc(nil, userJSON(c, viewer, true)), "X-OAuth-Scopes: repo, read:org, workflow\r\n")
		return
	case n == 2 && s(0) == "user" && s(1) == "repos":
		switch m {
		case "GET":
			listUserRepos(c)
		case "POST":
			postUserRepo(c)
		default:
			c.apiErr(404, "Not Found")
		}
		return
	case n >= 3 && s(0) == "repos":
	default:
		c.apiErr(404, "Not Found")
		return
	}
	// /repos/{owner}/{repo}/...
	key := p[1 : 1+len(seg[0])+1+len(seg[1])+1+len(seg[2])]
	key = key[len(seg[0])+1:]
	r := repos[string(key)]
	if r == nil {
		c.apiErr(404, "Not Found")
		return
	}
	if n == 3 {
		switch m {
		case "GET":
			c.obj(200, repoJSON(c, r))
		case "PATCH":
			patchRepo(c, r)
		case "DELETE":
			deleteRepo(r)
			c.respond(204, "", "X-GitHub-Request-Id: F0RG:DEL\r\n", nil)
		default:
			c.apiErr(404, "Not Found")
		}
		return
	}
	rest := ""
	if n > 4 {
		// everything after /repos/o/r/<kind>/, percent-decoded
		off := 1 + len(seg[0]) + 1 + len(seg[1]) + 1 + len(seg[2]) + 1 + len(seg[3]) + 1
		raw := string(bytes.Trim(p, "/")[off-1:])
		if strings.IndexByte(raw, '%') >= 0 {
			if d, err := url.PathUnescape(raw); err == nil {
				raw = d
			}
		}
		rest = raw
	}
	switch s(3) {
	case "issues":
		issuesRoute(c, r, n, s)
	case "pulls":
		pullsRoute(c, r, n, s)
	case "labels":
		labelsRoute(c, r, n, rest)
	case "contents":
		contentsRoute(c, r, rest)
	case "branches":
		branchesRoute(c, r, n, rest)
	case "commits":
		commitsRoute(c, r, n, rest)
	case "git":
		gitRefsRoute(c, r, rest)
	default:
		c.apiErr(404, "Not Found")
	}
}

// decode parses a JSON request body into v; false (after a 400) when it is malformed.
func decode(c *conn, v any) bool {
	body := c.r.body
	if len(bytes.TrimSpace(body)) == 0 {
		body = []byte("{}")
	}
	if err := json.Unmarshal(body, v); err != nil {
		c.apiErr(400, "Problems parsing JSON")
		return false
	}
	return true
}

func validation(c *conn, resource, field, code string) {
	c.obj(422, O{{"message", "Validation Failed"}, {"errors", []any{O{{"resource", resource}, {"code", code}, {"field", field}}}},
		{"documentation_url", "https://docs.github.com/rest"}})
}

// ---------------------------------------------------------------- shapes

func userJSON(c *conn, u User, full bool) O {
	base := c.baseURL()
	o := O{{"login", u.login}, {"id", u.id}, {"node_id", nodeID("U", u.id)},
		{"avatar_url", ""}, {"url", base + "/api/v3/users/" + u.login}, {"html_url", base + "/" + u.login},
		{"type", "User"}, {"site_admin", false}}
	if full {
		o = append(o, KV{"name", u.name}, KV{"email", u.email})
	}
	return o
}

func ownerJSON(c *conn) O {
	return O{{"login", viewer.login}, {"id", viewer.id}, {"node_id", nodeID("U", viewer.id)}, {"type", "User"}, {"site_admin", false}}
}

func repoJSON(c *conn, r *Repo) O {
	base := c.baseURL()
	api := base + "/api/v3/repos/" + r.full
	vis := "public"
	if r.private {
		vis = "private"
	}
	open := 0
	for _, is := range r.issueList {
		if is.state == "open" {
			open++
		}
	}
	var desc any
	if r.desc != "" {
		desc = r.desc
	}
	return O{{"id", r.id}, {"node_id", nodeID("R", r.id)}, {"name", r.name}, {"full_name", r.full}, {"private", r.private},
		{"owner", ownerJSON(c)}, {"html_url", base + "/" + r.full}, {"description", desc}, {"fork", false},
		{"url", api}, {"clone_url", base + "/" + r.full + ".git"}, {"created_at", r.created}, {"updated_at", nowStr},
		{"pushed_at", nowStr}, {"size", 0}, {"stargazers_count", 0}, {"watchers_count", 0}, {"has_issues", true},
		{"has_projects", true}, {"has_wiki", true}, {"forks_count", 0}, {"archived", false}, {"disabled", false},
		{"open_issues_count", open}, {"visibility", vis}, {"default_branch", r.defBranch},
		{"permissions", O{{"admin", true}, {"maintain", true}, {"push", true}, {"triage", true}, {"pull", true}}}}
}

func labelJSON(c *conn, r *Repo, l *Label) O {
	var desc any
	if l.desc != "" {
		desc = l.desc
	}
	return O{{"id", l.id}, {"node_id", nodeID("LA", l.id)}, {"url", c.baseURL() + "/api/v3/repos/" + r.full + "/labels/" + url.PathEscape(l.name)},
		{"name", l.name}, {"color", l.color}, {"default", false}, {"description", desc}}
}

func issueJSON(c *conn, is *Issue) []byte {
	if is.js != nil {
		return is.js
	}
	r := is.repo
	api := c.baseURL() + "/api/v3/repos/" + r.full
	labels := make([]any, 0, len(is.labels))
	for _, l := range is.labels {
		labels = append(labels, labelJSON(c, r, l))
	}
	var closed, reason any
	if is.closedAt != "" {
		closed = is.closedAt
	}
	if is.stateReason != "" {
		reason = is.stateReason
	}
	num := strconv.Itoa(is.num)
	is.js = enc(make([]byte, 0, 900), O{{"url", api + "/issues/" + num}, {"repository_url", api},
		{"html_url", c.baseURL() + "/" + r.full + "/issues/" + num}, {"id", is.id}, {"node_id", nodeID("I", is.id)},
		{"number", is.num}, {"title", is.title}, {"user", ownerJSON(c)}, {"labels", labels}, {"state", is.state},
		{"locked", false}, {"assignee", nil}, {"assignees", []any{}}, {"milestone", nil}, {"comments", len(is.comments)},
		{"created_at", is.created}, {"updated_at", is.updated}, {"closed_at", closed}, {"author_association", "OWNER"},
		{"body", is.body}, {"state_reason", reason}})
	return is.js
}

func commentJSON(c *conn, r *Repo, cm *Comment, num int) O {
	api := c.baseURL() + "/api/v3/repos/" + r.full
	return O{{"id", cm.id}, {"node_id", nodeID("IC", cm.id)}, {"url", api + "/issues/comments/" + strconv.Itoa(cm.id)},
		{"html_url", c.baseURL() + "/" + r.full + "/issues/" + strconv.Itoa(num)}, {"issue_url", api + "/issues/" + strconv.Itoa(num)},
		{"user", ownerJSON(c)}, {"created_at", cm.created}, {"updated_at", cm.created}, {"author_association", "OWNER"}, {"body", cm.body}}
}

func branchRefJSON(c *conn, r *Repo, branch string, sha oid) O {
	return O{{"label", r.owner + ":" + branch}, {"ref", branch}, {"sha", hexOID(sha)}, {"user", ownerJSON(c)},
		{"repo", O{{"id", r.id}, {"node_id", nodeID("R", r.id)}, {"name", r.name}, {"full_name", r.full}, {"private", r.private},
			{"owner", ownerJSON(c)}, {"default_branch", r.defBranch}}}}
}

func pullJSON(c *conn, p *Pull) O {
	r := p.repo
	api := c.baseURL() + "/api/v3/repos/" + r.full
	num := strconv.Itoa(p.num)
	var mergedAt, closedAt, mergeSha, mergeable any
	mstate := "unknown"
	if p.mergedAt != "" {
		mergedAt = p.mergedAt
	}
	if p.closedAt != "" {
		closedAt = p.closedAt
	}
	if p.merged {
		mergeSha = hexOID(p.mergeSha)
	} else if p.state == "open" {
		ok := p.mergeable()
		mergeable = ok
		if ok {
			mstate = "clean"
		} else {
			mstate = "dirty"
		}
	}
	files := p.files()
	add, del := 0, 0
	for _, f := range files {
		add += f.additions
		del += f.deletions
	}
	return O{{"url", api + "/pulls/" + num}, {"id", p.id}, {"node_id", nodeID("PR", p.id)},
		{"html_url", c.baseURL() + "/" + r.full + "/pull/" + num}, {"number", p.num}, {"state", p.state}, {"locked", false},
		{"title", p.title}, {"user", ownerJSON(c)}, {"body", p.body}, {"labels", []any{}}, {"created_at", p.created},
		{"updated_at", p.updated}, {"closed_at", closedAt}, {"merged_at", mergedAt}, {"merge_commit_sha", mergeSha},
		{"draft", p.draft}, {"head", branchRefJSON(c, r, p.head, p.headTip())}, {"base", branchRefJSON(c, r, p.base, p.baseTip())},
		{"author_association", "OWNER"}, {"merged", p.merged}, {"mergeable", mergeable}, {"mergeable_state", mstate},
		{"merged_by", nil}, {"comments", len(p.comments)}, {"commits", 1}, {"additions", add}, {"deletions", del},
		{"changed_files", len(files)}}
}

func identJSON(ident string) O {
	// "Name <email> 1700000000 +0000"
	name, email, date := ident, "", nowStr
	if lt := strings.IndexByte(ident, '<'); lt >= 0 {
		name = strings.TrimSpace(ident[:lt])
		if gt := strings.IndexByte(ident[lt:], '>'); gt >= 0 {
			email = ident[lt+1 : lt+gt]
			f := strings.Fields(ident[lt+gt+1:])
			if len(f) > 0 {
				if sec, err := strconv.ParseInt(f[0], 10, 64); err == nil {
					date = isoTime(sec)
				}
			}
		}
	}
	return O{{"name", name}, {"email", email}, {"date", date}}
}

func commitJSON(c *conn, r *Repo, id oid) O {
	ci, _ := commitOf(id)
	api := c.baseURL() + "/api/v3/repos/" + r.full
	sha := hexOID(id)
	parents := make([]any, 0, len(ci.parents))
	for _, p := range ci.parents {
		parents = append(parents, O{{"sha", hexOID(p)}, {"url", api + "/commits/" + hexOID(p)}})
	}
	return O{{"sha", sha}, {"node_id", nodeID("C", int(id[0])<<16|int(id[1])<<8|int(id[2]))}, {"url", api + "/commits/" + sha},
		{"html_url", c.baseURL() + "/" + r.full + "/commit/" + sha},
		{"commit", O{{"author", identJSON(ci.author)}, {"committer", identJSON(ci.committer)},
			{"message", strings.TrimRight(ci.message, "\n")}, {"tree", O{{"sha", hexOID(ci.tree)}, {"url", api + "/git/trees/" + hexOID(ci.tree)}}},
			{"comment_count", 0}}},
		{"author", ownerJSON(c)}, {"committer", ownerJSON(c)}, {"parents", parents}}
}

// ---------------------------------------------------------------- pagination

func pageArgs(q url.Values) (perPage, page int) {
	perPage, page = 30, 1
	if v, err := strconv.Atoi(q.Get("per_page")); err == nil && v > 0 {
		perPage = v
		if perPage > 100 {
			perPage = 100
		}
	}
	if v, err := strconv.Atoi(q.Get("page")); err == nil && v > 0 {
		page = v
	}
	return
}

// paginate slices total items and returns [from,to) and the Link header (empty when one page).
func paginate(c *conn, q url.Values, total int) (int, int, string) {
	perPage, page := pageArgs(q)
	from := (page - 1) * perPage
	if from > total {
		from = total
	}
	to := from + perPage
	if to > total {
		to = total
	}
	last := (total + perPage - 1) / perPage
	if last < 1 {
		last = 1
	}
	link := func(pg int, rel string) string {
		qq := url.Values{}
		for k, v := range q {
			qq[k] = v
		}
		qq.Set("per_page", strconv.Itoa(perPage))
		qq.Set("page", strconv.Itoa(pg))
		return "<" + c.baseURL() + string(c.r.path) + "?" + qq.Encode() + ">; rel=\"" + rel + "\""
	}
	var parts []string
	if page < last {
		parts = append(parts, link(page+1, "next"), link(last, "last"))
	}
	if page > 1 {
		parts = append(parts, link(1, "first"), link(page-1, "prev"))
	}
	if len(parts) == 0 {
		return from, to, ""
	}
	return from, to, "Link: " + strings.Join(parts, ", ") + "\r\n"
}

func query(c *conn) url.Values {
	q, _ := url.ParseQuery(string(c.r.query))
	return q
}

// ---------------------------------------------------------------- repositories

func listUserRepos(c *conn) {
	q := query(c)
	list := append([]*Repo(nil), repoList...)
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].full) < strings.ToLower(list[j].full) })
	from, to, link := paginate(c, q, len(list))
	out := make([]any, 0, to-from)
	for _, r := range list[from:to] {
		out = append(out, repoJSON(c, r))
	}
	c.api(200, enc(nil, out), link)
}

func postUserRepo(c *conn) {
	var in struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Private     *bool   `json:"private"`
		Visibility  string  `json:"visibility"`
		AutoInit    bool    `json:"auto_init"`
	}
	if !decode(c, &in) {
		return
	}
	if in.Name == nil || *in.Name == "" || !validRepoName(*in.Name) {
		validation(c, "Repository", "name", "missing_field")
		return
	}
	if repos[viewer.login+"/"+*in.Name] != nil {
		c.obj(422, O{{"message", "Repository creation failed."}, {"errors", []any{O{{"resource", "Repository"}, {"code", "custom"},
			{"field", "name"}, {"message", "name already exists on this account"}}}}, {"documentation_url", "https://docs.github.com/rest"}})
		return
	}
	desc := ""
	if in.Description != nil {
		desc = *in.Description
	}
	private := in.Private != nil && *in.Private || in.Visibility == "private"
	r := createRepo(*in.Name, desc, private)
	if in.AutoInit {
		blob := putObject(tBlob, []byte("# "+r.name+"\n"))
		tree := writeTree([]treeEntry{{"100644", "README.md", blob}})
		r.refs[r.head] = commitNow(tree, nil, "Initial commit\n")
		r.refsMoved()
	}
	c.obj(201, repoJSON(c, r))
}

func validRepoName(s string) bool {
	if len(s) > 100 || strings.HasSuffix(s, ".git") {
		return false
	}
	for _, ch := range s {
		if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.') {
			return false
		}
	}
	return true
}

func patchRepo(c *conn, r *Repo) {
	var in struct {
		Name          *string `json:"name"`
		Description   *string `json:"description"`
		Private       *bool   `json:"private"`
		DefaultBranch *string `json:"default_branch"`
	}
	if !decode(c, &in) {
		return
	}
	if in.Description != nil {
		r.desc = *in.Description
	}
	if in.Private != nil {
		r.private = *in.Private
	}
	if in.DefaultBranch != nil {
		if _, ok := r.branchTip(*in.DefaultBranch); ok {
			r.defBranch, r.head = *in.DefaultBranch, "refs/heads/"+*in.DefaultBranch
			r.refsMoved()
		}
	}
	if in.Name != nil && *in.Name != r.name && validRepoName(*in.Name) && repos[viewer.login+"/"+*in.Name] == nil {
		delete(repos, r.full)
		r.name, r.full = *in.Name, viewer.login+"/"+*in.Name
		repos[r.full] = r
	}
	c.obj(200, repoJSON(c, r))
}

// ---------------------------------------------------------------- labels

func labelsRoute(c *conn, r *Repo, n int, rest string) {
	switch {
	case n == 4 && c.r.method == "GET":
		out := make([]any, 0, len(r.labels))
		for _, l := range r.labels {
			out = append(out, labelJSON(c, r, l))
		}
		c.obj(200, out)
	case n == 4 && c.r.method == "POST":
		var in struct {
			Name        string `json:"name"`
			Color       string `json:"color"`
			Description string `json:"description"`
		}
		if !decode(c, &in) {
			return
		}
		if in.Name == "" {
			validation(c, "Label", "name", "missing_field")
			return
		}
		if r.label(in.Name) != nil {
			validation(c, "Label", "name", "already_exists")
			return
		}
		if in.Color == "" {
			in.Color = "ededed"
		}
		l := &Label{id: newID(), name: in.Name, color: strings.TrimPrefix(in.Color, "#"), desc: in.Description}
		r.labels = append(r.labels, l)
		c.obj(201, labelJSON(c, r, l))
	case n > 4 && c.r.method == "GET":
		if l := r.label(rest); l != nil {
			c.obj(200, labelJSON(c, r, l))
			return
		}
		c.apiErr(404, "Not Found")
	default:
		c.apiErr(404, "Not Found")
	}
}

// labelsFrom resolves label names (strings or {name} objects): the labels, or the first unknown name.
func labelsFrom(r *Repo, raw []any) ([]*Label, string) {
	var out []*Label
	for _, v := range raw {
		name := ""
		switch x := v.(type) {
		case string:
			name = x
		case map[string]any:
			name, _ = x["name"].(string)
		}
		l := r.label(name)
		if l == nil {
			return nil, name
		}
		out = append(out, l)
	}
	return out, ""
}

// ---------------------------------------------------------------- issues

func issuesRoute(c *conn, r *Repo, n int, s func(int) string) {
	m := c.r.method
	if n == 4 {
		switch m {
		case "GET":
			listIssues(c, r)
		case "POST":
			postIssue(c, r)
		default:
			c.apiErr(404, "Not Found")
		}
		return
	}
	num, err := strconv.Atoi(s(4))
	if err != nil {
		c.apiErr(404, "Not Found")
		return
	}
	is := r.issues[num]
	if is == nil {
		c.apiErr(404, "Not Found")
		return
	}
	switch {
	case n == 5 && m == "GET":
		c.api(200, issueJSON(c, is), "")
	case n == 5 && m == "PATCH":
		patchIssue(c, r, is)
	case n == 6 && s(5) == "comments" && m == "GET":
		q := query(c)
		from, to, link := paginate(c, q, len(is.comments))
		out := make([]any, 0, to-from)
		for _, cm := range is.comments[from:to] {
			out = append(out, commentJSON(c, r, cm, is.num))
		}
		c.api(200, enc(nil, out), link)
	case n == 6 && s(5) == "comments" && m == "POST":
		var in struct {
			Body *string `json:"body"`
		}
		if !decode(c, &in) {
			return
		}
		if in.Body == nil {
			validation(c, "IssueComment", "body", "missing_field")
			return
		}
		cm := addComment(is, nil, *in.Body)
		c.obj(201, commentJSON(c, r, cm, is.num))
	default:
		c.apiErr(404, "Not Found")
	}
}

func postIssue(c *conn, r *Repo) {
	var in struct {
		Title  *string `json:"title"`
		Body   *string `json:"body"`
		Labels []any   `json:"labels"`
	}
	if !decode(c, &in) {
		return
	}
	if in.Title == nil || *in.Title == "" {
		validation(c, "Issue", "title", "missing_field")
		return
	}
	labels, bad := labelsFrom(r, in.Labels)
	if bad != "" || labels == nil && len(in.Labels) > 0 {
		validation(c, "Label", "labels", "invalid")
		return
	}
	body := ""
	if in.Body != nil {
		body = *in.Body
	}
	is := newIssue(r, *in.Title, body, labels)
	c.api(201, issueJSON(c, is), "")
}

func patchIssue(c *conn, r *Repo, is *Issue) {
	var in struct {
		Title       *string `json:"title"`
		Body        *string `json:"body"`
		State       *string `json:"state"`
		StateReason *string `json:"state_reason"`
		Labels      *[]any  `json:"labels"`
	}
	if !decode(c, &in) {
		return
	}
	if in.Labels != nil {
		labels, bad := labelsFrom(r, *in.Labels)
		if bad != "" {
			validation(c, "Label", "labels", "invalid")
			return
		}
		is.labels = labels
	}
	if in.Title != nil {
		is.title = *in.Title
	}
	if in.Body != nil {
		is.body = *in.Body
	}
	if in.State != nil {
		if *in.State != "open" && *in.State != "closed" {
			validation(c, "Issue", "state", "invalid")
			return
		}
		reason := ""
		if in.StateReason != nil {
			reason = *in.StateReason
		}
		if *in.State != is.state || reason != "" {
			is.setState(*in.State, reason)
		}
	}
	is.js = nil
	c.api(200, issueJSON(c, is), "")
}

func listIssues(c *conn, r *Repo) {
	q := query(c)
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	var want []string
	if l := q.Get("labels"); l != "" {
		want = strings.Split(l, ",")
	}
	list := make([]*Issue, 0, len(r.issueList))
outer:
	for _, is := range r.issueList {
		if state != "all" && is.state != state {
			continue
		}
		for _, w := range want {
			found := false
			for _, l := range is.labels {
				if strings.EqualFold(l.name, strings.TrimSpace(w)) {
					found = true
				}
			}
			if !found {
				continue outer
			}
		}
		list = append(list, is)
	}
	asc := q.Get("direction") == "asc"
	switch q.Get("sort") {
	case "comments":
		sort.SliceStable(list, func(i, j int) bool {
			if asc {
				return len(list[i].comments) < len(list[j].comments)
			}
			return len(list[i].comments) > len(list[j].comments)
		})
	default: // created and updated both follow creation order here
		if !asc {
			for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
				list[i], list[j] = list[j], list[i]
			}
		}
	}
	from, to, link := paginate(c, q, len(list))
	b := make([]byte, 0, 1024*(to-from+1))
	b = append(b, '[')
	for i, is := range list[from:to] {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, issueJSON(c, is)...)
	}
	b = append(b, ']')
	c.api(200, b, link)
}

// ---------------------------------------------------------------- pull requests

func pullsRoute(c *conn, r *Repo, n int, s func(int) string) {
	m := c.r.method
	if n == 4 {
		switch m {
		case "GET":
			listPulls(c, r)
		case "POST":
			postPull(c, r)
		default:
			c.apiErr(404, "Not Found")
		}
		return
	}
	num, err := strconv.Atoi(s(4))
	p := r.pulls[num]
	if err != nil || p == nil {
		c.apiErr(404, "Not Found")
		return
	}
	switch {
	case n == 5 && m == "GET":
		c.obj(200, pullJSON(c, p))
	case n == 5 && m == "PATCH":
		var in struct {
			Title *string `json:"title"`
			Body  *string `json:"body"`
			State *string `json:"state"`
			Base  *string `json:"base"`
		}
		if !decode(c, &in) {
			return
		}
		if in.Title != nil {
			p.title = *in.Title
		}
		if in.Body != nil {
			p.body = *in.Body
		}
		if in.Base != nil {
			p.base = *in.Base
		}
		if in.State != nil && !p.merged {
			p.state = *in.State
			p.closedAt = ""
			if p.state == "closed" {
				p.closedAt = nowStr
			}
		}
		c.obj(200, pullJSON(c, p))
	case n == 6 && s(5) == "files" && m == "GET":
		files := p.files()
		q := query(c)
		from, to, link := paginate(c, q, len(files))
		out := make([]any, 0, to-from)
		for _, f := range files[from:to] {
			out = append(out, fileJSON(c, r, f, hexOID(p.headTip())))
		}
		c.api(200, enc(nil, out), link)
	case n == 6 && s(5) == "commits" && m == "GET":
		out := []any{}
		head, base := p.headTip(), p.baseTip()
		inBase := ancestors(base)
		for _, id := range walkCommits(head, 250) {
			if inBase[id] {
				continue
			}
			out = append(out, commitJSON(c, r, id))
		}
		for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
			out[i], out[j] = out[j], out[i]
		}
		c.obj(200, out)
	case n == 6 && s(5) == "merge" && m == "GET":
		if p.merged {
			c.respond(204, "", "X-GitHub-Request-Id: F0RG:MRG\r\n", nil)
		} else {
			c.apiErr(404, "Not Found")
		}
	case n == 6 && s(5) == "merge" && m == "PUT":
		var in struct {
			MergeMethod   string `json:"merge_method"`
			CommitTitle   string `json:"commit_title"`
			CommitMessage string `json:"commit_message"`
			Sha           string `json:"sha"`
		}
		if !decode(c, &in) {
			return
		}
		if in.Sha != "" && !strings.EqualFold(in.Sha, hexOID(p.headTip())) {
			c.apiErr(409, "Head branch was modified. Review and try the merge again.")
			return
		}
		sha, status, msg := p.merge(in.MergeMethod, in.CommitTitle, in.CommitMessage)
		if status != 200 {
			c.apiErr(status, msg)
			return
		}
		c.obj(200, O{{"sha", hexOID(sha)}, {"merged", true}, {"message", "Pull Request successfully merged"}})
	default:
		c.apiErr(404, "Not Found")
	}
}

func fileJSON(c *conn, r *Repo, f changedFile, ref string) O {
	api := c.baseURL() + "/api/v3/repos/" + r.full
	return O{{"sha", hexOID(f.sha)}, {"filename", f.path}, {"status", f.status}, {"additions", f.additions},
		{"deletions", f.deletions}, {"changes", f.additions + f.deletions},
		{"blob_url", c.baseURL() + "/" + r.full + "/blob/" + ref + "/" + f.path},
		{"raw_url", c.baseURL() + "/" + r.full + "/raw/" + ref + "/" + f.path},
		{"contents_url", api + "/contents/" + f.path + "?ref=" + ref}}
}

func postPull(c *conn, r *Repo) {
	var in struct {
		Title *string `json:"title"`
		Body  string  `json:"body"`
		Head  string  `json:"head"`
		Base  string  `json:"base"`
		Draft bool    `json:"draft"`
	}
	if !decode(c, &in) {
		return
	}
	if in.Title == nil || in.Head == "" || in.Base == "" {
		validation(c, "PullRequest", "title", "missing_field")
		return
	}
	p, why := newPull(r, *in.Title, in.Body, in.Head, in.Base, in.Draft)
	if p == nil {
		if why == "exists" {
			c.obj(422, O{{"message", "Validation Failed"}, {"errors", []any{O{{"resource", "PullRequest"}, {"code", "custom"},
				{"message", "A pull request already exists for " + r.owner + ":" + in.Head + "."}}}}})
			return
		}
		validation(c, "PullRequest", why, "invalid")
		return
	}
	c.obj(201, pullJSON(c, p))
}

func listPulls(c *conn, r *Repo) {
	q := query(c)
	state := q.Get("state")
	if state == "" {
		state = "open"
	}
	head := q.Get("head")
	if i := strings.IndexByte(head, ':'); i >= 0 {
		head = head[i+1:]
	}
	base := q.Get("base")
	var list []*Pull
	for _, p := range r.pullList {
		if state != "all" && p.state != state || head != "" && p.head != head || base != "" && p.base != base {
			continue
		}
		list = append(list, p)
	}
	if q.Get("direction") != "asc" {
		for i, j := 0, len(list)-1; i < j; i, j = i+1, j-1 {
			list[i], list[j] = list[j], list[i]
		}
	}
	from, to, link := paginate(c, q, len(list))
	out := make([]any, 0, to-from)
	for _, p := range list[from:to] {
		out = append(out, pullJSON(c, p))
	}
	c.api(200, enc(nil, out), link)
}

// ---------------------------------------------------------------- branches, refs and commits

func branchesRoute(c *conn, r *Repo, n int, rest string) {
	if c.r.method != "GET" {
		c.apiErr(404, "Not Found")
		return
	}
	api := c.baseURL() + "/api/v3/repos/" + r.full
	if n == 4 {
		var out []any
		for _, name := range r.sortedRefs() {
			if !strings.HasPrefix(name, "refs/heads/") {
				continue
			}
			sha := hexOID(r.refs[name])
			out = append(out, O{{"name", name[11:]}, {"commit", O{{"sha", sha}, {"url", api + "/commits/" + sha}}}, {"protected", false}})
		}
		if out == nil {
			out = []any{}
		}
		q := query(c)
		from, to, link := paginate(c, q, len(out))
		c.api(200, enc(nil, out[from:to]), link)
		return
	}
	tip, ok := r.branchTip(rest)
	if !ok {
		c.apiErr(404, "Branch not found")
		return
	}
	c.obj(200, O{{"name", rest}, {"commit", commitJSON(c, r, tip)}, {"protected", false},
		{"_links", O{{"self", api + "/branches/" + rest}, {"html", c.baseURL() + "/" + r.full + "/tree/" + rest}}}})
}

func refJSON(c *conn, r *Repo, name string, id oid) O {
	api := c.baseURL() + "/api/v3/repos/" + r.full
	t := "commit"
	if o := objects[id]; o != nil {
		t = typeName[o.t]
	}
	return O{{"ref", name}, {"node_id", nodeID("REF", r.id)}, {"url", api + "/git/" + name},
		{"object", O{{"sha", hexOID(id)}, {"type", t}, {"url", api + "/git/commits/" + hexOID(id)}}}}
}

func gitRefsRoute(c *conn, r *Repo, rest string) {
	m := c.r.method
	switch {
	case rest == "refs" && m == "POST":
		var in struct {
			Ref string `json:"ref"`
			Sha string `json:"sha"`
		}
		if !decode(c, &in) {
			return
		}
		id, ok := parseOID(strings.ToLower(in.Sha))
		if !strings.HasPrefix(in.Ref, "refs/") || strings.Count(in.Ref, "/") < 2 {
			c.apiErr(422, "Reference name must start with 'refs/' and have at least two slashes.")
			return
		}
		if !ok || objects[id] == nil {
			c.apiErr(422, "Object does not exist")
			return
		}
		if _, exists := r.refs[in.Ref]; exists {
			c.apiErr(422, "Reference already exists")
			return
		}
		r.refs[in.Ref] = id
		r.refsMoved()
		c.obj(201, refJSON(c, r, in.Ref, id))
	case rest == "refs" && m == "GET":
		var out []any
		for _, name := range r.sortedRefs() {
			out = append(out, refJSON(c, r, name, r.refs[name]))
		}
		if out == nil {
			out = []any{}
		}
		c.obj(200, out)
	case strings.HasPrefix(rest, "refs/") || strings.HasPrefix(rest, "ref/"):
		name := "refs/" + rest[strings.IndexByte(rest, '/')+1:]
		id, ok := r.refs[name]
		switch m {
		case "GET":
			if !ok {
				c.apiErr(404, "Not Found")
				return
			}
			c.obj(200, refJSON(c, r, name, id))
		case "DELETE":
			if !ok {
				c.apiErr(422, "Reference does not exist")
				return
			}
			delete(r.refs, name)
			r.refsMoved()
			c.respond(204, "", "X-GitHub-Request-Id: F0RG:REF\r\n", nil)
		case "PATCH":
			var in struct {
				Sha   string `json:"sha"`
				Force bool   `json:"force"`
			}
			if !decode(c, &in) {
				return
			}
			nid, valid := parseOID(strings.ToLower(in.Sha))
			if !ok || !valid || objects[nid] == nil {
				c.apiErr(422, "Reference does not exist")
				return
			}
			if !in.Force && !isAncestor(id, nid) {
				c.apiErr(422, "Update is not a fast forward")
				return
			}
			r.refs[name] = nid
			r.refsMoved()
			c.obj(200, refJSON(c, r, name, nid))
		default:
			c.apiErr(404, "Not Found")
		}
	default:
		c.apiErr(404, "Not Found")
	}
}

// walkCommits lists up to max commits reachable from tip, newest first by commit time.
func walkCommits(tip oid, max int) []oid {
	type cand struct {
		id   oid
		when int64
	}
	when := func(id oid) int64 {
		ci, _ := commitOf(id)
		f := strings.Fields(ci.committer)
		if len(f) >= 2 {
			v, _ := strconv.ParseInt(f[len(f)-2], 10, 64)
			return v
		}
		return 0
	}
	seen := map[oid]bool{tip: true}
	frontier := []cand{{tip, when(tip)}}
	var out []oid
	for len(frontier) > 0 && len(out) < max {
		best := 0
		for i := range frontier {
			if frontier[i].when > frontier[best].when {
				best = i
			}
		}
		cur := frontier[best]
		frontier = append(frontier[:best], frontier[best+1:]...)
		if objects[cur.id] == nil {
			continue
		}
		out = append(out, cur.id)
		ci, _ := commitOf(cur.id)
		for _, p := range ci.parents {
			if !seen[p] {
				seen[p] = true
				frontier = append(frontier, cand{p, when(p)})
			}
		}
	}
	return out
}

func commitsRoute(c *conn, r *Repo, n int, rest string) {
	if c.r.method != "GET" {
		c.apiErr(404, "Not Found")
		return
	}
	if n > 4 {
		id, ok := r.resolve(rest)
		if !ok {
			c.apiErr(404, "No commit found for SHA: "+rest)
			return
		}
		c.obj(200, commitJSON(c, r, id))
		return
	}
	q := query(c)
	tip, ok := r.resolve(q.Get("sha"))
	if !ok {
		if len(r.refs) == 0 {
			c.apiErr(409, "Git Repository is empty.")
		} else {
			c.apiErr(404, "Not Found")
		}
		return
	}
	perPage, page := pageArgs(q)
	ids := walkCommits(tip, perPage*page+1)
	if path := q.Get("path"); path != "" {
		var keep []oid
		for _, id := range ids {
			ci, _ := commitOf(id)
			cur, _ := lookupPath(ci.tree, path)
			var prev treeEntry
			if len(ci.parents) > 0 {
				prev, _ = lookupPath(treeOfCommit(ci.parents[0]), path)
			}
			if cur.id != prev.id {
				keep = append(keep, id)
			}
		}
		ids = keep
	}
	from, to, link := paginate(c, q, len(ids))
	out := make([]any, 0, to-from)
	for _, id := range ids[from:to] {
		out = append(out, commitJSON(c, r, id))
	}
	c.api(200, enc(nil, out), link)
}

// ---------------------------------------------------------------- contents

func contentEntry(c *conn, r *Repo, path string, e treeEntry, ref string) O {
	api := c.baseURL() + "/api/v3/repos/" + r.full
	t, size := "file", 0
	if isDir(e.mode) {
		t = "dir"
	} else if o := objects[e.id]; o != nil {
		size = len(o.data)
	}
	name := path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		name = path[i+1:]
	}
	self := api + "/contents/" + path + "?ref=" + ref
	var download any
	if t == "file" {
		download = c.baseURL() + "/" + r.full + "/raw/" + ref + "/" + path
	}
	return O{{"type", t}, {"name", name}, {"path", path}, {"sha", hexOID(e.id)}, {"size", size},
		{"url", self}, {"html_url", c.baseURL() + "/" + r.full + "/blob/" + ref + "/" + path},
		{"git_url", api + "/git/blobs/" + hexOID(e.id)}, {"download_url", download},
		{"_links", O{{"self", self}, {"git", api + "/git/blobs/" + hexOID(e.id)}}}}
}

func b64Wrapped(data []byte) string {
	s := base64.StdEncoding.EncodeToString(data)
	var b strings.Builder
	for len(s) > 60 {
		b.WriteString(s[:60])
		b.WriteByte('\n')
		s = s[60:]
	}
	b.WriteString(s)
	b.WriteByte('\n')
	return b.String()
}

func contentsRoute(c *conn, r *Repo, path string) {
	path = strings.Trim(path, "/")
	switch c.r.method {
	case "GET":
		q := query(c)
		ref := q.Get("ref")
		tip, ok := r.resolve(ref)
		if !ok {
			if len(r.refs) == 0 {
				c.apiErr(404, "This repository is empty.")
			} else {
				c.apiErr(404, "No commit found for the ref "+ref)
			}
			return
		}
		if ref == "" {
			ref = r.defBranch
		}
		e, ok := lookupPath(treeOfCommit(tip), path)
		if !ok {
			c.apiErr(404, "Not Found")
			return
		}
		if isDir(e.mode) {
			out := []any{}
			for _, ch := range treeOf(e.id) {
				p := ch.name
				if path != "" {
					p = path + "/" + ch.name
				}
				out = append(out, contentEntry(c, r, p, ch, ref))
			}
			c.obj(200, out)
			return
		}
		o := contentEntry(c, r, path, e, ref)
		o = append(o, KV{"content", b64Wrapped(objects[e.id].data)}, KV{"encoding", "base64"})
		c.obj(200, o)
	case "PUT", "DELETE":
		writeContents(c, r, path)
	default:
		c.apiErr(404, "Not Found")
	}
}

func writeContents(c *conn, r *Repo, path string) {
	var in struct {
		Message *string `json:"message"`
		Content *string `json:"content"`
		Sha     string  `json:"sha"`
		Branch  string  `json:"branch"`
	}
	if !decode(c, &in) {
		return
	}
	del := c.r.method == "DELETE"
	if path == "" || in.Message == nil || !del && in.Content == nil {
		field := "message"
		if in.Message != nil {
			field = "content"
		}
		validation(c, "Contents", field, "missing_field")
		return
	}
	var data []byte
	if !del {
		raw := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == ' ' {
				return -1
			}
			return r
		}, *in.Content)
		d, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			if d, err = base64.RawStdEncoding.DecodeString(raw); err != nil {
				c.apiErr(422, "content is not valid Base64")
				return
			}
		}
		data = d
	}
	branch := in.Branch
	if branch == "" {
		branch = r.defBranch
	}
	tip, hasTip := r.branchTip(branch)
	if !hasTip && len(r.refs) > 0 {
		if _, ok := r.branchTip(r.defBranch); ok || del {
			c.apiErr(404, "Branch "+branch+" not found")
			return
		}
	}
	var root oid
	if hasTip {
		root = treeOfCommit(tip)
	}
	cur, exists := lookupPath(root, path)
	exists = exists && !isDir(cur.mode)
	switch {
	case del && !exists:
		c.apiErr(404, "Not Found")
		return
	case exists && in.Sha == "":
		c.apiErr(422, "Invalid request.\n\n\"sha\" wasn't supplied.")
		return
	case exists && !strings.EqualFold(in.Sha, hexOID(cur.id)):
		c.apiErr(409, path+" does not match "+in.Sha)
		return
	}
	segs := splitPath(path)
	var blob oid
	if !del {
		blob = putObject(tBlob, data)
	}
	tree := setPath(root, segs, blob)
	var parents []oid
	if hasTip {
		parents = []oid{tip}
	}
	msg := *in.Message
	if !strings.HasSuffix(msg, "\n") {
		msg += "\n"
	}
	sha := commitNow(tree, parents, msg)
	r.refs["refs/heads/"+branch] = sha
	r.refsMoved()
	commit := commitJSON(c, r, sha)
	cj := O{{"sha", hexOID(sha)}, {"node_id", commit.get("node_id")}, {"url", commit.get("url")}, {"html_url", commit.get("html_url")}}
	inner := commit.get("commit").(O)
	cj = append(cj, KV{"author", inner.get("author")}, KV{"committer", inner.get("committer")}, KV{"tree", inner.get("tree")},
		KV{"message", inner.get("message")}, KV{"parents", commit.get("parents")},
		KV{"verification", O{{"verified", false}, {"reason", "unsigned"}, {"signature", nil}, {"payload", nil}}})
	if del {
		c.obj(200, O{{"content", nil}, {"commit", cj}})
		return
	}
	status := 201
	if exists {
		status = 200
	}
	ce := append(contentEntry(c, r, path, treeEntry{"100644", segs[len(segs)-1], blob}, branch), KV{"encoding", "base64"})
	c.obj(status, O{{"content", ce}, {"commit", cj}})
}
