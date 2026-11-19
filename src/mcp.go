package main

// Model Context Protocol at POST /mcp: JSON-RPC 2.0 over Streamable HTTP, answering application/json.
// It speaks revision 2026-07-28 (server/discover, no session) and the handshake revisions through
// initialize. Its tools take github-mcp-server's argument names and act on the store REST serves.

import (
	"encoding/json"
	"strconv"
	"strings"
)

var mcpVersions = []string{"2026-07-28", "2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

type tool struct {
	name, desc string
	props      []string // "name:type:description"
	required   []string
}

var tools = []tool{
	{"create_repo", "Create a new repository for the authenticated user.", []string{"name:string:Repository name", "description:string:Repository description", "private:boolean:Whether the repository is private", "autoInit:boolean:Initialize with a README"}, []string{"name"}},
	{"get_repo", "Get a repository by owner and name.", []string{"owner:string:Repository owner", "repo:string:Repository name"}, []string{"owner", "repo"}},
	{"create_issue", "Create a new issue in a repository.", []string{"owner:string:Repository owner", "repo:string:Repository name", "title:string:Issue title", "body:string:Issue body", "labels:array:Label names"}, []string{"owner", "repo", "title"}},
	{"get_issue", "Get one issue of a repository.", []string{"owner:string:Repository owner", "repo:string:Repository name", "issue_number:number:Issue number"}, []string{"owner", "repo", "issue_number"}},
	{"list_issues", "List issues in a repository.", []string{"owner:string:Repository owner", "repo:string:Repository name", "state:string:open, closed or all"}, []string{"owner", "repo"}},
	{"add_issue_comment", "Add a comment to an issue.", []string{"owner:string:Repository owner", "repo:string:Repository name", "issue_number:number:Issue number", "body:string:Comment text"}, []string{"owner", "repo", "issue_number", "body"}},
	{"create_pull_request", "Open a pull request from a head branch into a base branch.", []string{"owner:string:Repository owner", "repo:string:Repository name", "title:string:Pull request title", "head:string:Branch with the changes", "base:string:Branch to merge into", "body:string:Pull request description", "draft:boolean:Open as a draft"}, []string{"owner", "repo", "title", "head", "base"}},
	{"get_pull_request", "Get one pull request of a repository.", []string{"owner:string:Repository owner", "repo:string:Repository name", "pull_number:number:Pull request number"}, []string{"owner", "repo", "pull_number"}},
	{"list_pull_requests", "List pull requests in a repository.", []string{"owner:string:Repository owner", "repo:string:Repository name", "state:string:open, closed or all"}, []string{"owner", "repo"}},
	{"merge_pull_request", "Merge a pull request.", []string{"owner:string:Repository owner", "repo:string:Repository name", "pull_number:number:Pull request number", "merge_method:string:merge, squash or rebase", "commit_title:string:Title of the merge commit", "commit_message:string:Message of the merge commit"}, []string{"owner", "repo", "pull_number"}},
}

// toolsJSON is built once, so every tools/list answers the same bytes.
var toolsJSON = func() R {
	var list []any
	for _, t := range tools {
		props := O{}
		for _, p := range t.props {
			f := strings.SplitN(p, ":", 3)
			sch := O{{"type", f[1]}, {"description", f[2]}}
			if f[1] == "array" {
				sch = append(sch, KV{"items", O{{"type", "string"}}})
			}
			props = append(props, KV{f[0], sch})
		}
		list = append(list, O{{"name", t.name}, {"description", t.desc},
			{"inputSchema", O{{"type", "object"}, {"properties", props}, {"required", t.required}}}})
	}
	return R(enc(nil, list))
}()

var serverInfo = O{{"name", "forge-go"}, {"version", "1.0.0"}}

type rpcReq struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

func rpcResult(id json.RawMessage, result any) []byte {
	return enc(nil, O{{"jsonrpc", "2.0"}, {"id", R(id)}, {"result", result}})
}

func rpcError(id json.RawMessage, code int, msg string) []byte {
	if id == nil {
		id = json.RawMessage("null")
	}
	return enc(nil, O{{"jsonrpc", "2.0"}, {"id", R(id)}, {"error", O{{"code", code}, {"message", msg}}}})
}

func mcpHandler(c *conn) {
	switch c.r.method {
	case "POST":
	case "DELETE":
		c.respond(204, "", "", nil)
		return
	default:
		c.respond(405, jsonType, "Allow: POST, DELETE\r\n", rpcError(nil, -32000, "Method not allowed"))
		return
	}
	if !authed(c) {
		c.respond(401, jsonType, "WWW-Authenticate: Bearer\r\n", rpcError(nil, -32001, "Requires authentication"))
		return
	}
	var rq rpcReq
	if err := json.Unmarshal(c.r.body, &rq); err != nil || rq.Method == "" && rq.ID == nil {
		c.respond(400, jsonType, "", rpcError(nil, -32700, "Parse error"))
		return
	}
	if rq.ID == nil {
		// a notification: accepted, nothing to answer
		c.respond(202, "", "", nil)
		return
	}
	var params map[string]json.RawMessage
	json.Unmarshal(rq.Params, &params)
	var out []byte
	switch rq.Method {
	case "server/discover":
		out = rpcResult(rq.ID, O{{"supportedVersions", mcpVersions}, {"capabilities", O{{"tools", O{{"listChanged", false}}}}},
			{"serverInfo", serverInfo}, {"instructions", "A GitHub-compatible forge: repositories, issues and pull requests."},
			{"_meta", O{{"io.modelcontextprotocol/serverInfo", serverInfo}}}})
	case "initialize":
		var want string
		json.Unmarshal(params["protocolVersion"], &want)
		version := mcpVersions[1]
		for _, v := range mcpVersions[1:] {
			if v == want {
				version = v
			}
		}
		out = rpcResult(rq.ID, O{{"protocolVersion", version}, {"capabilities", O{{"tools", O{{"listChanged", false}}}}}, {"serverInfo", serverInfo}})
	case "ping":
		out = rpcResult(rq.ID, O{})
	case "tools/list":
		out = rpcResult(rq.ID, O{{"tools", toolsJSON}})
	case "tools/call":
		var name string
		json.Unmarshal(params["name"], &name)
		var args map[string]any
		json.Unmarshal(params["arguments"], &args)
		mu.Lock()
		out = callTool(c, rq.ID, name, args)
		mu.Unlock()
	default:
		out = rpcError(rq.ID, -32601, "Method not found: "+rq.Method)
	}
	c.respond(200, jsonType, "", out)
}

func toolText(id json.RawMessage, v any, isErr bool) []byte {
	text := string(enc(nil, v))
	if s, ok := v.(string); ok {
		text = s
	}
	res := O{{"content", []any{O{{"type", "text"}, {"text", text}}}}}
	if isErr {
		res = append(res, KV{"isError", true})
	}
	return rpcResult(id, res)
}

func callTool(c *conn, id json.RawMessage, name string, args map[string]any) []byte {
	var t *tool
	for i := range tools {
		if strings.EqualFold(tools[i].name, name) {
			t = &tools[i]
		}
	}
	if t == nil {
		return rpcError(id, -32602, "Unknown tool: "+name)
	}
	for _, req := range t.required {
		if _, ok := args[req]; !ok {
			return rpcError(id, -32602, "Invalid params: missing required argument '"+req+"' for "+t.name)
		}
	}
	str := func(k string) string {
		switch v := args[k].(type) {
		case string:
			return v
		case float64:
			return strconv.FormatFloat(v, 'f', -1, 64)
		}
		return ""
	}
	num := func(k string) int { n, _ := strconv.Atoi(str(k)); return n }
	fail := func(msg string) []byte { return toolText(id, msg, true) }
	var repo *Repo
	if _, ok := args["owner"]; ok {
		if str("owner") == viewer.login {
			repo = repos[viewer.login+"/"+str("repo")]
		}
		if repo == nil {
			return fail("repository " + str("owner") + "/" + str("repo") + " not found")
		}
	}
	switch t.name {
	case "create_repo":
		if !validRepoName(str("name")) {
			return fail("invalid repository name")
		}
		if repos[viewer.login+"/"+str("name")] != nil {
			return fail("name already exists on this account")
		}
		private, _ := args["private"].(bool)
		return toolText(id, repoJSON(c, createRepo(str("name"), str("description"), private)), false)
	case "get_repo":
		return toolText(id, repoJSON(c, repo), false)
	case "create_issue":
		var raw []any
		if l, ok := args["labels"].([]any); ok {
			raw = l
		}
		labels, bad := labelsFrom(repo, raw)
		if bad != "" {
			return fail("label " + bad + " does not exist")
		}
		if str("title") == "" {
			return fail("title is required")
		}
		return toolText(id, R(issueJSON(c, newIssue(repo, str("title"), str("body"), labels))), false)
	case "get_issue":
		is := repo.issues[num("issue_number")]
		if is == nil {
			return fail("issue not found")
		}
		return toolText(id, R(issueJSON(c, is)), false)
	case "list_issues":
		state := strings.ToLower(str("state"))
		if state == "" {
			state = "open"
		}
		b := []byte{'['}
		first := true
		for _, is := range repo.issueList {
			if state != "all" && is.state != state {
				continue
			}
			if !first {
				b = append(b, ',')
			}
			first = false
			b = append(b, issueJSON(c, is)...)
		}
		return toolText(id, R(append(b, ']')), false)
	case "add_issue_comment":
		is := repo.issues[num("issue_number")]
		if is == nil {
			return fail("issue not found")
		}
		return toolText(id, commentJSON(c, repo, addComment(is, nil, str("body")), is.num), false)
	case "create_pull_request":
		draft, _ := args["draft"].(bool)
		p, why := newPull(repo, str("title"), str("body"), str("head"), str("base"), draft)
		if p == nil {
			return fail("cannot open the pull request: " + why)
		}
		return toolText(id, pullJSON(c, p), false)
	case "get_pull_request":
		p := repo.pulls[num("pull_number")]
		if p == nil {
			return fail("pull request not found")
		}
		return toolText(id, pullJSON(c, p), false)
	case "list_pull_requests":
		state := strings.ToLower(str("state"))
		if state == "" {
			state = "open"
		}
		var list []any
		for _, p := range repo.pullList {
			if state == "all" || p.state == state {
				list = append(list, pullJSON(c, p))
			}
		}
		if list == nil {
			list = []any{}
		}
		return toolText(id, list, false)
	case "merge_pull_request":
		p := repo.pulls[num("pull_number")]
		if p == nil {
			return fail("pull request not found")
		}
		method := str("merge_method")
		if method == "" {
			method = "squash"
		}
		sha, status, msg := p.merge(method, str("commit_title"), str("commit_message"))
		if status != 200 {
			return fail(msg)
		}
		return toolText(id, O{{"sha", hexOID(sha)}, {"merged", true}, {"message", "Pull Request successfully merged"}}, false)
	}
	return rpcError(id, -32602, "Unknown tool: "+name)
}

