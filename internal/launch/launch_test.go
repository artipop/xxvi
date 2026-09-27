package launch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/artipop/xxvi/internal/model"
)

func write(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDetect(t *testing.T) {
	cases := []struct {
		name  string
		files map[string]string
		kind  string
		tool  string
		cmd   string
	}{
		{"vite with pnpm, nothing installed", map[string]string{
			"package.json":   `{"scripts":{"dev":"vite"},"devDependencies":{"vite":"5"}}`,
			"pnpm-lock.yaml": "",
		}, model.LaunchWeb, "vite", "pnpm install && pnpm run dev"},
		{"electron claims a web project", map[string]string{
			"package.json":       `{"scripts":{"start":"electron ."},"dependencies":{"electron":"30","vite":"5"}}`,
			"node_modules/.keep": "",
		}, model.LaunchDesktop, "electron", "npm run start"},
		{"compose", map[string]string{"compose.yaml": "services: {}"}, model.LaunchBackend, "compose", "docker compose up --build"},
		{"dockerfile publishes what it exposes", map[string]string{"Dockerfile": "FROM x\nEXPOSE 8081\n"},
			model.LaunchBackend, "docker", "-p 8081:8081"},
		{"go service", map[string]string{
			"go.mod":  "module x\n",
			"main.go": "package main\nimport \"net/http\"\nfunc main(){ http.ListenAndServe(\":8080\", nil) }\n",
		}, model.LaunchBackend, "go", "go run ."},
		{"flutter", map[string]string{"pubspec.yaml": "flutter:\n  sdk: flutter\n"}, model.LaunchMobile, "flutter", "flutter run"},
		{"expo", map[string]string{"package.json": `{"dependencies":{"expo":"50","react-native":"0.7"}}`},
			model.LaunchMobile, "expo", "expo start"},
		{"wails v3", map[string]string{"go.mod": "module x\nrequire github.com/wailsapp/wails/v3 v3.0.0\n"},
			model.LaunchDesktop, "wails", "wails3 dev"},
		{"frontend in a subfolder", map[string]string{
			"frontend/package.json": `{"scripts":{"dev":"next dev"},"dependencies":{"next":"14"}}`,
		}, model.LaunchWeb, "next", "cd frontend && npm install && npm run dev"},
		{"static page", map[string]string{"index.html": "<p>hi</p>"}, model.LaunchWeb, "static", "http.server"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, c.files)
			got := Detect(dir)
			first := got[0]
			if first.Kind != c.kind || first.Tool != c.tool || !strings.Contains(first.Command, c.cmd) {
				t.Fatalf("first profile = %+v, want %s/%s with %q", first, c.kind, c.tool, c.cmd)
			}
			if last := got[len(got)-1]; last.Kind != model.LaunchCommand {
				t.Fatalf("a plain command must close the list, got %+v", last)
			}
		})
	}
}

func TestDetectEmpty(t *testing.T) {
	got := Detect(t.TempDir())
	if len(got) != 1 || got[0].Kind != model.LaunchCommand {
		t.Fatalf("an empty folder offers only a command, got %+v", got)
	}
}

func TestPrefer(t *testing.T) {
	in := []Profile{{Kind: "web", Tool: "a"}, {Kind: "backend", Tool: "b"}, {Kind: "backend", Tool: "c"}}
	got := Prefer(in, "backend")
	if got[0].Tool != "b" || got[1].Tool != "c" || got[2].Tool != "a" {
		t.Fatalf("got %+v", got)
	}
	if in[0].Tool != "a" {
		t.Fatal("Prefer must not reorder its argument")
	}
}

func TestAddress(t *testing.T) {
	cases := map[string]string{
		"\x1b[32m  ➜  Local:\x1b[0m   \x1b[36mhttp://localhost:\x1b[1m5174\x1b[22m/\x1b[39m\n": "http://localhost:5174/",
		"Listening on http://0.0.0.0:8000.":              "http://localhost:8000",
		"started server, url: http://127.0.0.1:3000/api": "http://127.0.0.1:3000/api",
		"see https://example.com/docs, nothing local":    "",
	}
	for in, want := range cases {
		if got := Address([]byte(in)); got != want {
			t.Errorf("Address(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSplit(t *testing.T) {
	work := Rect{X: 0, Y: 25, W: 1512, H: 900}
	ours, theirs := Split(work, model.LaunchDesktop)
	if ours.W+theirs.W != work.W || theirs.X != ours.X+ours.W || ours.H != work.H {
		t.Fatalf("the two must tile the work area: %+v %+v", ours, theirs)
	}
	if theirs.W < ours.W {
		t.Fatalf("a desktop application gets most of the screen: %+v %+v", ours, theirs)
	}
	_, phone := Split(work, model.LaunchMobile)
	if phone.W > 520 {
		t.Fatalf("a simulator gets a strip, got %d", phone.W)
	}
	small, _ := Split(Rect{W: 900, H: 600}, model.LaunchDesktop)
	if small.W < 480 {
		t.Fatalf("our window keeps its minimum, got %d", small.W)
	}
}
