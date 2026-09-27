// Package launch is how a project is started to be looked at: what it is,
// read off its files, and what to do with the window it opens.
//
// Detection only proposes. Every profile carries the command it would run, the
// run screen shows it in an editable field, and what a person settles on is
// remembered for the project: a guess made from file names is right often
// enough to save typing and wrong often enough that it must never be final.
package launch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/artipop/xxvi/internal/model"
)

// Profile is one way to start a project.
type Profile struct {
	Kind string `json:"kind"`
	// Tool is what the project was recognised by — «vite», «compose»,
	// «flutter» — shown as it is, next to the kind.
	Tool string `json:"tool,omitempty"`
	// Dir is the subfolder the project was found in, relative to the card's
	// folder, when it is not the folder itself: a repository with the
	// frontend in frontend/ is started from there.
	Dir     string `json:"dir,omitempty"`
	Command string `json:"command"`
	// URL is where the project is expected to answer before it has said so
	// itself. What it prints wins (Address).
	URL string `json:"url,omitempty"`
}

// subdirs are looked into besides the folder itself. Only one level: deeper
// is where vendored examples and test fixtures live, and a run screen that
// offers to start somebody's fixture is noise.
const maxSubdirs = 24

// Detect lists what the folder can be started as, the likeliest first. The
// folder's own findings come before its subfolders', and a plain command is
// always last, so the list is never empty.
func Detect(dir string) []Profile {
	var out []Profile
	out = append(out, detectIn(dir, "")...)
	entries, _ := os.ReadDir(dir)
	seen := 0
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || strings.HasPrefix(name, ".") || skipDir[name] {
			continue
		}
		if seen++; seen > maxSubdirs {
			break
		}
		out = append(out, detectIn(filepath.Join(dir, name), name)...)
	}
	return append(out, Profile{Kind: model.LaunchCommand})
}

var skipDir = map[string]bool{
	"node_modules": true, "vendor": true, "dist": true, "build": true, "target": true,
	"bin": true, "obj": true, "out": true, "docs": true, "Pods": true, "venv": true,
}

// Prefer moves the profiles of one kind to the front, keeping their order.
func Prefer(profiles []Profile, kind string) []Profile {
	if kind == "" {
		return profiles
	}
	out := make([]Profile, len(profiles))
	copy(out, profiles)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].Kind == kind && out[j].Kind != kind
	})
	return out
}

// detectIn is one folder. The order inside it is the order of confidence: an
// Electron or Expo project also has every mark of a web one, so what it is
// most specifically is asked first and claims the folder.
func detectIn(dir, rel string) []Profile {
	f := folder{dir: dir}
	pkg := f.packageJSON()
	var out []Profile
	add := func(p Profile) {
		p.Dir = rel
		if rel != "" && p.Command != "" {
			p.Command = "cd " + shellQuote(rel) + " && " + p.Command
		}
		out = append(out, p)
	}

	mobile := f.mobile(pkg)
	for _, p := range mobile {
		add(p)
	}
	desktop := f.desktop(pkg)
	for _, p := range desktop {
		add(p)
	}
	if len(mobile) == 0 && len(desktop) == 0 {
		if p, ok := f.web(pkg); ok {
			add(p)
		}
	}
	for _, p := range f.backend(pkg) {
		add(p)
	}
	return out
}

type folder struct{ dir string }

func (f folder) has(name string) bool {
	_, err := os.Stat(filepath.Join(f.dir, name))
	return err == nil
}

func (f folder) read(name string) string {
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil || len(b) > 1<<20 {
		return ""
	}
	return string(b)
}

func (f folder) glob(pattern string) []string {
	m, _ := filepath.Glob(filepath.Join(f.dir, pattern))
	return m
}

type pkgJSON struct {
	ok      bool
	deps    map[string]bool
	scripts map[string]string
}

func (f folder) packageJSON() pkgJSON {
	raw := f.read("package.json")
	if raw == "" {
		return pkgJSON{}
	}
	var p struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
		Scripts         map[string]string `json:"scripts"`
	}
	if json.Unmarshal([]byte(raw), &p) != nil {
		return pkgJSON{}
	}
	deps := map[string]bool{}
	for d := range p.Dependencies {
		deps[d] = true
	}
	for d := range p.DevDependencies {
		deps[d] = true
	}
	return pkgJSON{ok: true, deps: deps, scripts: p.Scripts}
}

func (p pkgJSON) any(names ...string) string {
	for _, n := range names {
		if p.deps[n] {
			return n
		}
	}
	return ""
}

// script is how the package manager this folder uses runs one of its scripts,
// with the install in front when nothing is installed yet: a branch checked
// out for review has never had one.
func (f folder) script(p pkgJSON, names ...string) (string, bool) {
	for _, name := range names {
		if _, ok := p.scripts[name]; ok {
			return f.npm(name), true
		}
	}
	return "", false
}

func (f folder) npm(script string) string {
	pm := "npm"
	switch {
	case f.has("pnpm-lock.yaml"):
		pm = "pnpm"
	case f.has("yarn.lock"):
		pm = "yarn"
	case f.has("bun.lockb"), f.has("bun.lock"):
		pm = "bun"
	}
	run := pm + " run " + script
	if pm == "yarn" {
		run = "yarn " + script
	}
	if !f.has("node_modules") {
		return pm + " install && " + run
	}
	return run
}

func (f folder) npx(args string) string {
	cmd := "npx " + args
	if !f.has("node_modules") {
		return f.npmInstall() + " && " + cmd
	}
	return cmd
}

func (f folder) npmInstall() string {
	switch {
	case f.has("pnpm-lock.yaml"):
		return "pnpm install"
	case f.has("yarn.lock"):
		return "yarn install"
	case f.has("bun.lockb"), f.has("bun.lock"):
		return "bun install"
	}
	return "npm install"
}

var mac = runtime.GOOS == "darwin"

func (f folder) mobile(pkg pkgJSON) []Profile {
	var out []Profile
	if strings.Contains(f.read("pubspec.yaml"), "flutter:") {
		cmd := "flutter run"
		if mac {
			cmd = "open -a Simulator && flutter run"
		}
		out = append(out, Profile{Kind: model.LaunchMobile, Tool: "flutter", Command: cmd})
	}
	if pkg.deps["expo"] {
		target := "--android"
		if mac {
			target = "--ios"
		}
		out = append(out, Profile{Kind: model.LaunchMobile, Tool: "expo", Command: f.npx("expo start " + target)})
	} else if pkg.deps["react-native"] {
		target := "run-android"
		if mac {
			target = "run-ios"
		}
		out = append(out, Profile{Kind: model.LaunchMobile, Tool: "react-native", Command: f.npx("react-native " + target)})
	}
	if p, ok := f.android(); ok {
		out = append(out, p)
	}
	if mac {
		if proj, sdk := f.xcode(); proj != "" && sdk == "iphoneos" {
			// Building and installing an Xcode project by hand needs its scheme
			// and bundle id, and both are better known to Xcode than to a
			// guess: the simulator comes up, and Run is one keystroke there.
			out = append(out, Profile{Kind: model.LaunchMobile, Tool: "xcode",
				Command: "open -a Simulator && open " + shellQuote(proj)})
		}
	}
	return out
}

var applicationID = regexp.MustCompile(`applicationId\s*=?\s*["']([\w.]+)["']`)

func (f folder) android() (Profile, bool) {
	var gradle string
	for _, name := range []string{"app/build.gradle", "app/build.gradle.kts", "build.gradle", "build.gradle.kts"} {
		if text := f.read(name); strings.Contains(text, "com.android.application") {
			gradle = text
			break
		}
	}
	if gradle == "" || !f.has("gradlew") {
		return Profile{}, false
	}
	// The emulator is started detached: it outlives the build, and a
	// terminal that ended with it would take the emulator down too.
	cmd := `(emulator -avd "$(emulator -list-avds | head -n 1)" >/dev/null 2>&1 &) ; ` +
		"adb wait-for-device && ./gradlew installDebug"
	if m := applicationID.FindStringSubmatch(gradle); m != nil {
		cmd += " && adb shell monkey -p " + m[1] + " -c android.intent.category.LAUNCHER 1"
	}
	return Profile{Kind: model.LaunchMobile, Tool: "android", Command: cmd}, true
}

var sdkRoot = regexp.MustCompile(`SDKROOT = (\w+);`)

// xcode finds an Xcode project and which platform it builds for.
func (f folder) xcode() (string, string) {
	projects := f.glob("*.xcodeproj")
	if len(projects) == 0 {
		return "", ""
	}
	open := filepath.Base(projects[0])
	if ws := f.glob("*.xcworkspace"); len(ws) > 0 {
		open = filepath.Base(ws[0])
	}
	pbx := folder{dir: projects[0]}.read("project.pbxproj")
	if m := sdkRoot.FindStringSubmatch(pbx); m != nil {
		return open, m[1]
	}
	return open, ""
}

var guiCrates = []string{"eframe", "egui", "iced", "slint", "gtk", "gtk4", "winit", "tauri", "druid", "fltk"}

func (f folder) desktop(pkg pkgJSON) []Profile {
	var out []Profile
	if f.has("wails.json") || strings.Contains(f.read("go.mod"), "github.com/wailsapp/wails") {
		cmd := "wails dev"
		if strings.Contains(f.read("go.mod"), "wailsapp/wails/v3") {
			cmd = "wails3 dev"
		}
		out = append(out, Profile{Kind: model.LaunchDesktop, Tool: "wails", Command: cmd})
	}
	if f.has("src-tauri") {
		out = append(out, Profile{Kind: model.LaunchDesktop, Tool: "tauri", Command: f.npx("tauri dev")})
	}
	if pkg.deps["electron"] {
		cmd, ok := f.script(pkg, "dev", "start")
		if !ok {
			cmd = f.npx("electron .")
		}
		out = append(out, Profile{Kind: model.LaunchDesktop, Tool: "electron", Command: cmd})
	}
	if mac {
		if proj, sdk := f.xcode(); proj != "" && sdk == "macosx" {
			out = append(out, Profile{Kind: model.LaunchDesktop, Tool: "xcode", Command: "open " + shellQuote(proj)})
		}
	}
	if cargo := f.read("Cargo.toml"); cargo != "" && !f.has("src-tauri") {
		for _, c := range guiCrates {
			if strings.Contains(cargo, "\n"+c+" ") || strings.Contains(cargo, "\n"+c+"=") {
				out = append(out, Profile{Kind: model.LaunchDesktop, Tool: c, Command: "cargo run"})
				break
			}
		}
	}
	return out
}

// webTools are dev servers by package, with the port each prints when left
// alone: what the preview opens before the server has said where it is.
var webTools = []struct {
	pkg  string
	tool string
	port int
}{
	{"next", "next", 3000},
	{"nuxt", "nuxt", 3000},
	{"@sveltejs/kit", "sveltekit", 5173},
	{"astro", "astro", 4321},
	{"@angular/core", "angular", 4200},
	{"react-scripts", "create-react-app", 3000},
	{"@remix-run/dev", "remix", 5173},
	{"vite", "vite", 5173},
	{"parcel", "parcel", 1234},
	{"webpack-dev-server", "webpack", 8080},
}

func (f folder) web(pkg pkgJSON) (Profile, bool) {
	if pkg.ok {
		for _, w := range webTools {
			if !pkg.deps[w.pkg] {
				continue
			}
			cmd, ok := f.script(pkg, "dev", "start", "serve")
			if !ok {
				continue
			}
			return Profile{Kind: model.LaunchWeb, Tool: w.tool, Command: cmd,
				URL: fmt.Sprintf("http://localhost:%d", w.port)}, true
		}
		return Profile{}, false
	}
	// A page with nothing to build is served rather than opened as a file:
	// scripts on a file:// page are refused half of what they ask for, and
	// the reviewer would be looking at a page that breaks where the real one
	// does not.
	if f.has("index.html") {
		return Profile{Kind: model.LaunchWeb, Tool: "static",
			Command: "python3 -m http.server 8000", URL: "http://localhost:8000"}, true
	}
	return Profile{}, false
}

var notName = regexp.MustCompile(`[^a-zA-Z0-9]+`)

var exposed = regexp.MustCompile(`(?mi)^\s*EXPOSE\s+(\d+)`)

func (f folder) backend(pkg pkgJSON) []Profile {
	var out []Profile
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yml", "docker-compose.yaml"} {
		if f.has(name) {
			out = append(out, Profile{Kind: model.LaunchBackend, Tool: "compose", Command: "docker compose up --build"})
			break
		}
	}
	if len(out) == 0 && f.has("Dockerfile") {
		image := "xxvi-run-" + strings.ToLower(notName.ReplaceAllString(filepath.Base(f.dir), "-"))
		ports, url := "", ""
		for _, m := range exposed.FindAllStringSubmatch(f.read("Dockerfile"), -1) {
			ports += " -p " + m[1] + ":" + m[1]
			if url == "" {
				url = "http://localhost:" + m[1]
			}
		}
		out = append(out, Profile{Kind: model.LaunchBackend, Tool: "docker", URL: url,
			Command: "docker build -t " + image + " . && docker run --rm -it" + ports + " " + image})
	}

	if gomod := f.read("go.mod"); gomod != "" && !strings.Contains(gomod, "wailsapp/wails") {
		if p, ok := f.goService(gomod); ok {
			out = append(out, p)
		}
	}
	if tool := pkg.any("@nestjs/core", "express", "fastify", "koa", "hono", "@hapi/hapi"); tool != "" {
		if cmd, ok := f.script(pkg, "start:dev", "dev", "start"); ok {
			if tool == "@nestjs/core" {
				tool = "nest"
			}
			out = append(out, Profile{Kind: model.LaunchBackend, Tool: tool, Command: cmd,
				URL: "http://localhost:3000"})
		}
	}
	python := strings.ToLower(f.read("pyproject.toml") + f.read("requirements.txt"))
	switch {
	case f.has("manage.py"):
		out = append(out, Profile{Kind: model.LaunchBackend, Tool: "django",
			Command: "python3 manage.py runserver", URL: "http://127.0.0.1:8000"})
	case strings.Contains(python, "fastapi"):
		module := "main:app"
		if f.has("app/main.py") {
			module = "app.main:app"
		}
		out = append(out, Profile{Kind: model.LaunchBackend, Tool: "fastapi",
			Command: "uvicorn " + module + " --reload", URL: "http://127.0.0.1:8000"})
	case strings.Contains(python, "flask"):
		out = append(out, Profile{Kind: model.LaunchBackend, Tool: "flask",
			Command: "flask run", URL: "http://127.0.0.1:5000"})
	}
	if pom := f.read("pom.xml"); strings.Contains(pom, "spring-boot") {
		cmd := "mvn spring-boot:run"
		if f.has("mvnw") {
			cmd = "./mvnw spring-boot:run"
		}
		out = append(out, Profile{Kind: model.LaunchBackend, Tool: "spring", Command: cmd, URL: "http://localhost:8080"})
	} else if strings.Contains(f.read("build.gradle")+f.read("build.gradle.kts"), "org.springframework.boot") {
		out = append(out, Profile{Kind: model.LaunchBackend, Tool: "spring", Command: "./gradlew bootRun", URL: "http://localhost:8080"})
	}
	if strings.Contains(f.read("Gemfile"), "rails") {
		out = append(out, Profile{Kind: model.LaunchBackend, Tool: "rails", Command: "bin/rails server", URL: "http://localhost:3000"})
	}
	return out
}

var goServers = regexp.MustCompile(`"(net/http|github\.com/gin-gonic/gin|github\.com/labstack/echo[^"]*|github\.com/gofiber/fiber[^"]*|github\.com/go-chi/chi[^"]*|google\.golang\.org/grpc)"`)

// goService is a Go module with a main package that serves something: a
// module that only builds a library or a CLI is nothing to look at.
func (f folder) goService(gomod string) (Profile, bool) {
	servesIn := func(dir string) bool {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		main, serves := false, false
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			b, err := os.ReadFile(file)
			if err != nil {
				continue
			}
			text := string(b)
			main = main || strings.Contains(text, "package main")
			serves = serves || goServers.MatchString(text)
		}
		return main && serves
	}
	if servesIn(f.dir) {
		return Profile{Kind: model.LaunchBackend, Tool: "go", Command: "go run .", URL: "http://localhost:8080"}, true
	}
	cmds, _ := os.ReadDir(filepath.Join(f.dir, "cmd"))
	for _, c := range cmds {
		if c.IsDir() && servesIn(filepath.Join(f.dir, "cmd", c.Name())) {
			return Profile{Kind: model.LaunchBackend, Tool: "go",
				Command: "go run ./cmd/" + c.Name(), URL: "http://localhost:8080"}, true
		}
	}
	return Profile{}, false
}

var plainWord = regexp.MustCompile(`^[\w./-]+$`)

// shellQuote keeps a folder name one argument: the command goes through the
// person's shell (internal/term), and a name with a space would be two.
func shellQuote(s string) string {
	if plainWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
