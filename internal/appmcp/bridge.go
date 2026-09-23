package appmcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// How an outside agent finds this application, and how it talks to it when all
// it can spawn is a command.
//
// The address changes on every launch — a loopback port is asked for, not
// chosen — while the line in somebody's agent configuration does not. So the
// running application leaves its address beside the database, and `xxvi mcp`
// reads it there and pipes one stdio session through to it. The bridge carries
// no tool schemas of its own: it asks the server what it has and forwards the
// calls, so there is one place the tools are described and it is the one that
// answers them.

// HandoffFile is where a running application says where its tools are. Beside
// the database, because that is the one place both halves agree on.
const HandoffFile = "mcp.json"

// Handoff is that file: an address and the token for it.
type Handoff struct {
	URL   string `json:"url"`
	Token string `json:"token"`
	// PID is whose address this is, so a file left behind by a killed
	// application can be told from a live one.
	PID int `json:"pid"`
}

// WriteHandoff leaves the address for `xxvi mcp` to find. Written 0600: it
// carries the token.
func WriteHandoff(dataDir, url, token string) error {
	if url == "" || token == "" {
		return fmt.Errorf("нечего записывать: инструменты приложения не поднялись")
	}
	body, err := json.MarshalIndent(Handoff{URL: url, Token: token, PID: os.Getpid()}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dataDir, HandoffFile), append(body, '\n'), 0o600)
}

// RemoveHandoff takes the address away when the application stops. An address
// that outlives its listener is a bridge failing with a connection error
// instead of with "приложение не запущено".
func RemoveHandoff(dataDir string) {
	_ = os.Remove(filepath.Join(dataDir, HandoffFile))
}

// ReadHandoff is the address a running application left.
func ReadHandoff(dataDir string) (Handoff, error) {
	body, err := os.ReadFile(filepath.Join(dataDir, HandoffFile))
	if err != nil {
		if os.IsNotExist(err) {
			return Handoff{}, fmt.Errorf("XXVI не запущен: инструменты приложения живут внутри него, откройте приложение")
		}
		return Handoff{}, err
	}
	var h Handoff
	if err := json.Unmarshal(body, &h); err != nil {
		return Handoff{}, fmt.Errorf("не удалось прочитать %s: %w", HandoffFile, err)
	}
	if h.URL == "" || h.Token == "" {
		return Handoff{}, fmt.Errorf("в %s нет адреса инструментов — перезапустите приложение", HandoffFile)
	}
	return h, nil
}

// ServeStdio is `xxvi mcp`: one stdio session bridged to the running
// application. It returns when the caller closes stdio, which is how an agent
// ends a session.
func ServeStdio(ctx context.Context, dataDir string, in *os.File, out *os.File) error {
	h, err := ReadHandoff(dataDir)
	if err != nil {
		return err
	}
	if err := Bridge(ctx, h, &mcp.IOTransport{Reader: in, Writer: out}); err != nil && !ended(err) {
		return err
	}
	return nil
}

// ended reports the session simply being over. An agent ends one by closing
// stdio, so that is success and not a failure to report: a non-zero exit there
// would make every finished session look like a broken server in somebody's
// agent log.
//
// The text is matched because the error underneath is a code of the SDK's
// internal jsonrpc2 package (ErrServerClosing), which is not ours to compare
// against. Narrow on purpose: everything it does not recognise is still an
// error.
func ended(err error) bool {
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrClosedPipe),
		errors.Is(err, context.Canceled), errors.Is(err, mcp.ErrConnectionClosed):
		return true
	}
	text := err.Error()
	return strings.Contains(text, "server is closing") || strings.Contains(text, "connection closed")
}

// Bridge pipes one session on transport through to the application at h.
func Bridge(ctx context.Context, h Handoff, transport mcp.Transport) error {
	client := mcp.NewClient(&mcp.Implementation{Name: ServerName + "-bridge", Version: "1"}, nil)
	upstream, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   h.URL,
		HTTPClient: &http.Client{Transport: bearer{token: h.Token}},
	}, nil)
	if err != nil {
		return fmt.Errorf("не удалось подключиться к XXVI: %w", err)
	}
	defer upstream.Close()

	tools, err := upstream.ListTools(ctx, nil)
	if err != nil {
		return fmt.Errorf("не удалось прочитать инструменты XXVI: %w", err)
	}
	srv := mcp.NewServer(
		&mcp.Implementation{Name: ServerName, Title: "XXVI", Version: "1"},
		&mcp.ServerOptions{Instructions: instructions},
	)
	for _, tool := range tools.Tools {
		name := tool.Name
		// The tool is passed on as it came, schema and all: this side knows
		// nothing about what it forwards, and that is what keeps the two
		// descriptions from drifting apart.
		srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return upstream.CallTool(ctx, &mcp.CallToolParams{
				Name: name, Arguments: req.Params.Arguments, Meta: req.Params.Meta,
			})
		})
	}
	return srv.Run(ctx, transport)
}

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

// Config is the line to put in an agent's MCP configuration: this executable,
// with `mcp` after it. Shown on the agents screen, so nobody has to know where
// the handoff file is.
func Config(binary string) string {
	if strings.TrimSpace(binary) == "" {
		binary, _ = os.Executable()
	}
	body, _ := json.MarshalIndent(map[string]any{"mcpServers": map[string]any{
		ServerName: map[string]any{"command": binary, "args": []string{"mcp"}},
	}}, "", "  ")
	return string(body)
}
