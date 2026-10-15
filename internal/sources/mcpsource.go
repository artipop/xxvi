package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"text/template"
	"time"

	"github.com/artipop/gandvik/internal/model"
	"github.com/artipop/gandvik/internal/msg"
	"github.com/artipop/gandvik/internal/sources/mcp"
	"github.com/artipop/gandvik/internal/sources/plugin"
)

// An MCP server as a source. What MCP lacks for this is one word: which tool is
// the feed and how to read a row of it. A manifest can say exactly that, so a
// service with an MCP server is a JSON file and not a program:
//
//	{"name": "tracker", "kind": "mcp",
//	 "command": "bun", "args": ["run", "server.ts"], "dir": "/path/to/it",
//	 "mcp": {"tool": "list_my_cards",
//	         "arguments": {"boardId": "{{.Config.boardId}}"},
//	         "itemsAt": "cards",
//	         "item": {"id": "{{.id}}", "title": "{{.title}}", "url": "{{.url}}"}}}

// MCPSpec is how one MCP server is read as a feed.
type MCPSpec struct {
	// Tool is the one to call, and Arguments are what to call it with,
	// templated over the source: a board typed into the source form reaches
	// the tool as {{.Config.board}}.
	Tool      string            `json:"tool"`
	Arguments map[string]string `json:"arguments,omitempty"`

	// ItemsAt is where the list is inside the answer, as a dotted path, or
	// empty when the answer is the list. Named rather than guessed: an answer
	// with three arrays in it is the normal case, and a guess would pick the
	// wrong one silently.
	ItemsAt string `json:"itemsAt,omitempty"`

	// Item maps one row onto an item, each field a text/template over the row.
	Item ItemTemplate `json:"item"`
}

// ItemTemplate is one row of a tool's answer, written as an item. Only the
// item's own fields: a mapping that could compute would be a program in a
// config file, and what is being mapped is a row of JSON with names on it.
type ItemTemplate struct {
	ID      string            `json:"id"`
	Version string            `json:"version,omitempty"`
	Title   string            `json:"title"`
	Body    string            `json:"body,omitempty"`
	URL     string            `json:"url,omitempty"`
	At      string            `json:"at,omitempty"`
	Props   map[string]string `json:"props,omitempty"`
	Labels  []string          `json:"labels,omitempty"`
}

// Validate refuses a mapping that cannot produce an item worth having.
func (s MCPSpec) Validate() (MCPSpec, error) {
	s.Tool = strings.TrimSpace(s.Tool)
	if s.Tool == "" {
		return s, msg.Err("mcp.noTool")
	}
	s.ItemsAt = strings.TrimSpace(s.ItemsAt)
	s.Item.Title = strings.TrimSpace(s.Item.Title)
	if s.Item.Title == "" {
		return s, msg.Err("mcp.noTitle", "tool", s.Tool)
	}
	// An id may be left out — the item is then known by the hash of what it
	// says — but a version without an id promises change detection that cannot
	// work: the pair is the key.
	s.Item.ID = strings.TrimSpace(s.Item.ID)
	s.Item.Version = strings.TrimSpace(s.Item.Version)
	if s.Item.ID == "" && s.Item.Version != "" {
		return s, msg.Err("mcp.versionWithoutID", "tool", s.Tool)
	}
	return s, nil
}

// mcpConn is an MCP server behind the interface the runner polls.
type mcpConn struct {
	client *mcp.Client
	spec   MCPSpec
	src    model.Source
}

// dialMCP starts an MCP server for a source and checks, once, that the tool the
// manifest names is there. At dial rather than at the first poll, because a
// manifest is written by hand: "the server has no list_my_cards, it has
// get_card, get_columns…" is an answer, and a poll failing every five minutes
// is not.
func dialMCP(ctx context.Context, src model.Source, manifest Manifest, cred plugin.Credentials) (conn, error) {
	client, err := openMCP(ctx, src, manifest, cred, manifest.MCP.Tool)
	if err != nil {
		return nil, err
	}
	return &mcpConn{client: client, spec: *manifest.MCP, src: src}, nil
}

// openMCP starts the server and checks that the tool asked for is one it has.
func openMCP(ctx context.Context, src model.Source, manifest Manifest, cred plugin.Credentials, tool string) (*mcp.Client, error) {
	env, err := manifest.RenderEnv(src)
	if err != nil {
		return nil, err
	}
	if cred.AccessToken != "" && strings.TrimSpace(manifest.TokenEnv) != "" {
		env[strings.TrimSpace(manifest.TokenEnv)] = cred.AccessToken
	}
	argv, err := manifest.Argv(src)
	if err != nil {
		return nil, err
	}
	client, err := mcp.Dial(ctx, mcp.Spec{
		Command:    argv,
		Dir:        manifest.Dir,
		Env:        envList(env),
		ClientName: "Gandvik",
	})
	if err != nil {
		return nil, err
	}
	tools, err := client.Tools(ctx)
	if err != nil {
		client.Close()
		return nil, err
	}
	if !hasTool(tools, tool) {
		client.Close()
		return nil, msg.Err("mcp.toolMissing", "plugin", manifest.Name, "tool", tool,
			"have", strings.Join(toolNames(tools), ", "))
	}
	return client, nil
}

// Option is one value a field can be given, and what to call it.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// fieldOptions asks the server for the values a field can take. A field whose
// dependencies are not filled yet has no answer rather than a wrong one: the
// columns of a board nobody has chosen are not every column there is.
func fieldOptions(ctx context.Context, src model.Source, manifest Manifest, cred plugin.Credentials, field Field) ([]Option, error) {
	o := field.Options
	if o == nil || !manifest.IsMCP() {
		return nil, msg.Err("source.noOptions", "plugin", manifest.Name, "field", field.Key)
	}
	for _, key := range o.DependsOn {
		if strings.TrimSpace(src.Config[key]) == "" {
			return nil, nil
		}
	}
	args, err := MCPSpec{Arguments: o.Arguments}.renderArguments(src)
	if err != nil {
		return nil, err
	}
	client, err := openMCP(ctx, src, manifest, cred, o.Tool)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	payload, err := client.CallTool(ctx, o.Tool, args)
	if err != nil {
		return nil, err
	}
	rows, err := rowsAt(payload, o.At)
	if err != nil {
		return nil, msg.Wrap(err, "mcp.badAnswer", "tool", o.Tool)
	}
	out := make([]Option, 0, len(rows))
	for _, row := range rows {
		value, err := renderOver(o.Value, row)
		if err != nil {
			continue
		}
		if value = strings.TrimSpace(value); value == "" {
			continue
		}
		label := value
		if o.Label != "" {
			if text, err := renderOver(o.Label, row); err == nil && strings.TrimSpace(text) != "" {
				label = strings.TrimSpace(text)
			}
		}
		out = append(out, Option{Value: value, Label: label})
	}
	return out, nil
}

func hasTool(tools []mcp.Tool, name string) bool {
	for _, tool := range tools {
		if strings.EqualFold(tool.Name, name) {
			return true
		}
	}
	return false
}

func toolNames(tools []mcp.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, tool := range tools {
		out = append(out, tool.Name)
	}
	return out
}

// Capabilities are the same for every MCP server: it answers when asked and
// never volunteers. There is no cursor — MCP has none — so the tool reports
// what it can see each time, and (id, version) keeps that from becoming
// duplicates.
func (c *mcpConn) Capabilities() plugin.Capabilities {
	return plugin.Capabilities{Poll: true}
}

func (c *mcpConn) Close() { c.client.Close() }

// Poll calls the tool and reads its answer as items.
func (c *mcpConn) Poll(ctx context.Context, _ string) (plugin.PollResult, error) {
	args, err := c.spec.renderArguments(c.src)
	if err != nil {
		return plugin.PollResult{}, err
	}
	payload, err := c.client.CallTool(ctx, c.spec.Tool, args)
	if err != nil {
		return plugin.PollResult{}, err
	}
	rows, err := rowsAt(payload, c.spec.ItemsAt)
	if err != nil {
		return plugin.PollResult{}, msg.Wrap(err, "mcp.badAnswer", "tool", c.spec.Tool)
	}
	items := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		// A row that cannot be read costs that row: a feed of fifty with one
		// oddity in it is still forty-nine cards.
		if item, err := c.spec.Item.render(row); err == nil {
			items = append(items, item)
		}
	}
	return plugin.PollResult{Items: items}, nil
}

// renderArguments expands the arguments over the source.
func (s MCPSpec) renderArguments(src model.Source) (map[string]any, error) {
	out := make(map[string]any, len(s.Arguments))
	for name, tmpl := range s.Arguments {
		value, err := renderOver(tmpl, src)
		if err != nil {
			return nil, msg.Wrap(err, "mcp.badArgument", "argument", name)
		}
		if value = strings.TrimSpace(value); value == "" {
			// A field nobody filled in: a tool asked for board "" answers for
			// no board at all, rather than for every board.
			continue
		}
		// A form has only strings, and MCP tools are schema-checked on the
		// server's side: "77" where 77 was declared is a refusal, not a
		// coercion. What looks like a number or a boolean is sent as one.
		switch {
		case strings.EqualFold(value, "true"):
			out[name] = true
		case strings.EqualFold(value, "false"):
			out[name] = false
		default:
			if n, err := strconv.ParseFloat(value, 64); err == nil && !strings.ContainsAny(value, " \t") {
				out[name] = n
			} else {
				out[name] = value
			}
		}
	}
	return out, nil
}

// render turns one row into item JSON — JSON rather than a struct because that
// is what every other plugin hands the runner, and one path through the decoder
// is one set of bugs.
func (t ItemTemplate) render(row any) (json.RawMessage, error) {
	item := model.Item{}
	fields := []struct {
		tmpl string
		into *string
	}{
		{t.ID, &item.ExternalID},
		{t.Version, &item.Version},
		{t.Title, &item.Title},
		{t.Body, &item.Body},
		{t.URL, &item.URL},
	}
	for _, field := range fields {
		value, err := renderOver(field.tmpl, row)
		if err != nil {
			return nil, err
		}
		*field.into = strings.TrimSpace(value)
	}
	if item.Title == "" {
		return nil, fmt.Errorf("the row has no title")
	}
	if t.At != "" {
		if at, err := renderOver(t.At, row); err == nil {
			if parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(at)); err == nil {
				item.At = parsed
			}
		}
	}
	for name, tmpl := range t.Props {
		value, err := renderOver(tmpl, row)
		if err != nil {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			if item.Props == nil {
				item.Props = map[string]string{}
			}
			item.Props[name] = value
		}
	}
	for _, tmpl := range t.Labels {
		value, err := renderOver(tmpl, row)
		if err != nil {
			continue
		}
		for _, label := range strings.Split(value, ",") {
			if label = strings.TrimSpace(label); label != "" {
				item.Labels = append(item.Labels, label)
			}
		}
	}
	if raw, err := json.Marshal(row); err == nil {
		item.Raw = raw
	}
	return json.Marshal(item)
}

// renderOver expands one template. A path that is not there renders empty
// rather than as Go's "<no value>": a row from somebody else's service is
// expected to be missing fields.
func renderOver(text string, data any) (string, error) {
	if !strings.Contains(text, "{{") {
		return text, nil
	}
	t, err := template.New("source").Option("missingkey=zero").Parse(text)
	if err != nil {
		return "", err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return strings.ReplaceAll(buf.String(), "<no value>", ""), nil
}

// rowsAt digs the list out of the answer, following a dotted path. A single
// object is read as a list of one: a tool that returns "the card" is as good a
// feed as one that returns "the cards".
func rowsAt(payload json.RawMessage, path string) ([]any, error) {
	var value any
	if err := json.Unmarshal(payload, &value); err != nil {
		return nil, err
	}
	for _, step := range strings.Split(path, ".") {
		if step = strings.TrimSpace(step); step == "" {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("no object at %q", path)
		}
		if value, ok = object[step]; !ok {
			return nil, fmt.Errorf("no %q in the answer", path)
		}
	}
	switch rows := value.(type) {
	case []any:
		return rows, nil
	case map[string]any:
		return []any{rows}, nil
	case nil:
		return nil, nil
	default:
		return nil, fmt.Errorf("%q is not a list", path)
	}
}

// envList is the environment in the "KEY=value" shape a process takes, sorted:
// a map has no order, and a command line should not change between runs.
func envList(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for name, value := range env {
		out = append(out, name+"="+value)
	}
	sort.Strings(out)
	return out
}
