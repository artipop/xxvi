package sources

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Manifests this application ships: a service it carries its own server for
// (`gandvik mcp kaiten`), or one whose server another application of ours
// carries. They are in the dialog from the first run with nothing to install.
//
//go:embed manifests/*.json
var builtinManifests embed.FS

// ManifestsDir is where a person's own manifests are read from, under the data
// directory. A file rather than a row, because a manifest for a server on this
// machine names a path on this machine, and because it has to be possible to
// write one before the app has any UI for it.
const ManifestsDir = "sources/manifests"

// Catalog is every manifest there is: the files in the data directory over the
// ones this app ships. A file wins over a built-in one of the same name — the
// way to change what a shipped manifest does is to write your own beside it.
type Catalog struct {
	builtins []Manifest

	mu        sync.RWMutex
	manifests []Manifest
	problems  []error
}

// LoadCatalog reads the built-in manifests and those in dir. A file that
// cannot be read or does not validate is reported and skipped: one bad
// manifest must not take the other sources down, and the app has to come up to
// be able to say what was wrong.
//
// Builtins are the sources the application reads with its own code — the demo
// file, the review queue — described the way a manifest describes a plugin, so
// the list of what a source can be made from is one list and the form has no
// source it knows by name. A manifest by one of their names is refused:
// otherwise a file in the data directory would take a built-in source over.
func LoadCatalog(dir string, builtins ...Manifest) *Catalog {
	c := &Catalog{}
	for _, m := range builtins {
		m.Kind = KindBuiltin
		valid, err := m.Validate()
		if err != nil {
			c.builtins = append(c.builtins, m)
			continue
		}
		c.builtins = append(c.builtins, valid)
	}
	c.Reload(dir)
	return c
}

// Reload reads the manifests again.
func (c *Catalog) Reload(dir string) {
	own, problems := readManifests(dir)
	shipped, bad := builtin()
	problems = append(problems, bad...)
	for _, m := range shipped {
		if _, ok := find(own, m.Name); !ok {
			own = append(own, m)
		}
	}
	kept := own[:0]
	for _, m := range own {
		if _, taken := find(c.builtins, m.Name); taken {
			problems = append(problems, fmt.Errorf("%q is a built-in source and cannot be a manifest", m.Name))
			continue
		}
		kept = append(kept, m)
	}
	own = append(kept, c.builtins...)
	sort.Slice(own, func(i, j int) bool { return strings.ToLower(own[i].Name) < strings.ToLower(own[j].Name) })

	c.mu.Lock()
	defer c.mu.Unlock()
	c.manifests, c.problems = own, problems
}

// Get returns a manifest by name.
func (c *Catalog) Get(name string) (Manifest, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return find(c.manifests, name)
}

// All returns every manifest, in name order.
func (c *Catalog) All() []Manifest {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Manifest(nil), c.manifests...)
}

// Problems are the files that were skipped, and why.
func (c *Catalog) Problems() []error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]error(nil), c.problems...)
}

func find(list []Manifest, name string) (Manifest, bool) {
	for _, m := range list {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return Manifest{}, false
}

func builtin() ([]Manifest, []error) {
	entries, err := builtinManifests.ReadDir("manifests")
	if err != nil {
		return nil, []error{err}
	}
	var (
		out  []Manifest
		errs []error
	)
	for _, entry := range entries {
		raw, err := builtinManifests.ReadFile("manifests/" + entry.Name())
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", entry.Name(), err))
			continue
		}
		m, err := parseManifest(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", entry.Name(), err))
			continue
		}
		out = append(out, m)
	}
	return out, errs
}

func readManifests(dir string) ([]Manifest, []error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{fmt.Errorf("%s: %w", dir, err)}
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.EqualFold(filepath.Ext(entry.Name()), ".json") {
			names = append(names, entry.Name())
		}
	}
	// Name order, so two files claiming one name resolve the same way on every
	// start rather than by whatever the filesystem listed first.
	sort.Strings(names)
	var (
		out  []Manifest
		errs []error
	)
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		m, err := parseManifest(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
			continue
		}
		if _, dup := find(out, m.Name); dup {
			errs = append(errs, fmt.Errorf("%s: another file already describes %q", name, m.Name))
			continue
		}
		out = append(out, m)
	}
	return out, errs
}

func parseManifest(raw []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, err
	}
	if strings.EqualFold(strings.TrimSpace(m.Kind), KindBuiltin) {
		return Manifest{}, fmt.Errorf("a file cannot describe a built-in source")
	}
	return m.Validate()
}
