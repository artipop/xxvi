package acp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/artipop/gandvik/internal/msg"
)

// Adapters installed by the application into a folder of its own.
//
// A start never goes to the registry: it runs whatever is installed. The
// version moves only when a person presses «Update», so a release of the
// adapter is a download they asked for rather than minutes of silence inside
// some other request — npx, which ran them before, checked for a newer
// release on every start and fetched it before the agent could say a word.

var (
	adaptersDir string
	// installing keeps two installs of one kind out of each other's way: the
	// tool writes into one folder, and a second press must not half-replace it.
	installing sync.Map // kind → *sync.Mutex
)

// KeepAdaptersIn sets the folder adapters are installed into. Unset, there is
// no install of ours and only what the machine has is found.
func KeepAdaptersIn(dir string) { adaptersDir = dir }

func installDir(kind string) string {
	if adaptersDir == "" {
		return ""
	}
	return filepath.Join(adaptersDir, kind)
}

// installedBin is our install of a kind's adapter, when there is one.
func installedBin(kind string) (string, bool) {
	def := adapters[kind]
	dir := installDir(kind)
	if dir == "" || def.pkg == "" {
		return "", false
	}
	bin := def.from.binIn(dir, def.bin)
	if _, err := os.Stat(bin); err != nil {
		return "", false
	}
	return bin, true
}

// InstallResult is what an install changed.
type InstallResult struct {
	Status AdapterStatus `json:"status"`
	// Was is the version before, empty on a first install.
	Was string `json:"was,omitempty"`
}

// Install puts the latest release of a kind's adapter into our folder, or
// moves an install of ours to it. A running agent keeps the code it started
// with; the next start gets the new one.
func Install(ctx context.Context, kind string) (InstallResult, error) {
	def, ok := adapters[kind]
	if !ok || def.pkg == "" {
		return InstallResult{}, msg.Err("adapter.notInstallable", "kind", kind)
	}
	dir := installDir(kind)
	if dir == "" {
		return InstallResult{}, msg.Err("adapter.notInstallable", "kind", kind)
	}
	tool, err := lookupBin(def.from.tool)
	if err != nil {
		return InstallResult{}, msg.Err("adapter.noTool", "bin", def.bin, "tool", def.from.tool,
			"needs", def.from.needs)
	}
	mu, _ := installing.LoadOrStore(kind, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()

	var was string
	if _, ok := installedBin(kind); ok {
		was = def.from.versionIn(dir, def.pkg)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return InstallResult{}, err
	}
	args, env := def.from.installArgs(def.pkg, dir)
	cmd := exec.CommandContext(ctx, tool, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		text := strings.TrimSpace(out.String())
		if text == "" {
			text = err.Error()
		}
		return InstallResult{}, msg.Err("adapter.installFailed", "bin", def.bin, "output", tail(text, 1500))
	}
	return InstallResult{Status: adapterStatus(kind), Was: was}, nil
}

// tail keeps the end of a tool's output: the error a failed install ends on is
// what explains it, and the progress before it does not.
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func npmVersion(dir, pkg string) string {
	body, err := os.ReadFile(filepath.Join(dir, "node_modules", pkg, "package.json"))
	if err != nil {
		return ""
	}
	var p struct {
		Version string `json:"version"`
	}
	_ = json.Unmarshal(body, &p)
	return p.Version
}

// pypiVersion reads the version from the dist-info folder in the tool's
// environment, which is named after the distribution and its version.
func pypiVersion(dir, pkg string) string {
	name := strings.NewReplacer("-", "_", ".", "_").Replace(strings.ToLower(pkg))
	found, _ := filepath.Glob(filepath.Join(dir, "tools", "*", "lib", "python*", "site-packages", "*.dist-info"))
	for _, d := range found {
		base := strings.TrimSuffix(filepath.Base(d), ".dist-info")
		dist, version, ok := strings.Cut(base, "-")
		if ok && strings.ToLower(dist) == name {
			return version
		}
	}
	return ""
}
