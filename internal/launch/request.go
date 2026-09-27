package launch

import (
	"context"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Request is one call to a started service, as the run screen composed it.
type Request struct {
	Method  string `json:"method"`
	URL     string `json:"url"`
	Headers string `json:"headers,omitempty"` // «Name: value», one per line
	Body    string `json:"body,omitempty"`
}

// Response is what came back, cut to what a screen can show.
type Response struct {
	Status    string `json:"status"`
	Code      int    `json:"code"`
	Headers   string `json:"headers"`
	Body      string `json:"body"`
	Truncated bool   `json:"truncated,omitempty"`
	Millis    int64  `json:"millis"`
}

const bodyCap = 512 << 10

// Send makes the request from here rather than from the page: the page lives
// on the application's own origin, and every service that does not answer
// CORS for it — which is every service — would look broken to the reviewer.
func Send(ctx context.Context, r Request) (Response, error) {
	method := strings.ToUpper(strings.TrimSpace(r.Method))
	if method == "" {
		method = http.MethodGet
	}
	var body io.Reader
	if r.Body != "" {
		body = strings.NewReader(r.Body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimSpace(r.URL), body)
	if err != nil {
		return Response{}, err
	}
	for _, line := range strings.Split(r.Headers, "\n") {
		name, value, ok := strings.Cut(line, ":")
		if ok && strings.TrimSpace(name) != "" {
			req.Header.Add(strings.TrimSpace(name), strings.TrimSpace(value))
		}
	}
	if r.Body != "" && req.Header.Get("Content-Type") == "" && strings.HasPrefix(strings.TrimSpace(r.Body), "{") {
		req.Header.Set("Content-Type", "application/json")
	}

	started := time.Now()
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, bodyCap+1))
	if err != nil {
		return Response{}, err
	}
	var headers strings.Builder
	for name, values := range resp.Header {
		for _, v := range values {
			headers.WriteString(name + ": " + v + "\n")
		}
	}
	out := Response{
		Status:  resp.Status,
		Code:    resp.StatusCode,
		Headers: headers.String(),
		Millis:  time.Since(started).Milliseconds(),
	}
	if len(data) > bodyCap {
		data, out.Truncated = data[:bodyCap], true
	}
	out.Body = string(data)
	return out, nil
}

// Clients are the HTTP clients a person may prefer to the screen's own form.
// Only the installed ones are offered: a button that opens nothing is worse
// than no button.
var clients = []string{"Postman", "Insomnia", "Bruno", "Yaak", "HTTPie"}

// Clients lists the installed ones. Asked of the application folders on macOS
// and of PATH elsewhere, where they install as commands.
func Clients() []string {
	var out []string
	for _, name := range clients {
		if clientInstalled(name) {
			out = append(out, name)
		}
	}
	return out
}

func clientInstalled(name string) bool {
	if runtime.GOOS == "darwin" {
		home, _ := os.UserHomeDir()
		for _, dir := range []string{"/Applications", filepath.Join(home, "Applications")} {
			if _, err := os.Stat(filepath.Join(dir, name+".app")); err == nil {
				return true
			}
		}
		return false
	}
	_, err := exec.LookPath(strings.ToLower(name))
	return err == nil
}

// OpenClient starts one of Clients. The address is not handed over: none of
// them takes one on its command line, so the screen puts it on the clipboard.
func OpenClient(name string) error {
	known := false
	for _, c := range clients {
		known = known || c == name
	}
	if !known {
		return os.ErrNotExist
	}
	if runtime.GOOS == "darwin" {
		return exec.Command("open", "-a", name).Start()
	}
	cmd := exec.Command(strings.ToLower(name))
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// Docker says whether docker answers here, which is what makes a compose
// profile worth offering first.
func Docker() bool {
	_, err := exec.LookPath("docker")
	return err == nil
}
