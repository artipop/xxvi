// Package buildversion knows every place this repository writes its own
// version number, so that "what version is this" has one answer rather than
// seven files that agree until somebody edits six of them.
//
// It exists as a package rather than as a script because two callers need the
// same list: cmd/setversion, which writes the number, and the test beside
// version.go, which fails when the files disagree. A list held by only one of
// them drifts from the other, which is the failure this whole package is about.
//
// The obvious alternative — `wails3 task common:update:build-assets`, which
// regenerates the platform assets from build/config.yml — writes the assets
// from the CLI's own templates, so every hand-written line in them goes with
// it. The number is the only thing that has to move on a release, so only the
// number is moved.
package buildversion

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// A Site is one file and the pattern that finds the version in it. The pattern
// must have exactly one capturing group, around the version and nothing else:
// that group is what Set replaces and what Read returns.
type Site struct {
	Path    string
	Pattern *regexp.Regexp

	// Why this file carries the number, for whoever adds the next one.
	Why string
}

// Sites is every file that states the version, in the order a person would
// think of them. Adding a platform means adding a line here; nothing else in
// the tree knows the list.
var Sites = []Site{{
	Path:    "version.go",
	Pattern: regexp.MustCompile(`const appVersion = "([^"]*)"`),
	Why:     "what the running binary calls itself, and what the updater compares",
}, {
	Path:    "build/config.yml",
	Pattern: regexp.MustCompile(`(?m)^  version: "([^"]*)"`),
	Why:     "the wails3 project config every build asset is generated from",
}, {
	Path:    "build/darwin/Info.plist",
	Pattern: regexp.MustCompile(`(?s)<key>CFBundle(?:ShortVersionString|Version)</key>\s*<string>([^<]*)</string>`),
	Why:     "the .app bundle's own version, which Finder and Gatekeeper read",
}, {
	Path:    "build/darwin/Info.dev.plist",
	Pattern: regexp.MustCompile(`(?s)<key>CFBundle(?:ShortVersionString|Version)</key>\s*<string>([^<]*)</string>`),
	Why:     "the same for the bundle `wails3 dev` runs",
}, {
	Path:    "build/windows/info.json",
	Pattern: regexp.MustCompile(`"(?:file_version|ProductVersion)": "([^"]*)"`),
	Why:     "what goes into the .exe's version resource (.syso)",
}, {
	Path:    "build/windows/nsis/wails_tools.nsh",
	Pattern: regexp.MustCompile(`!define INFO_PRODUCTVERSION "([^"]*)"`),
	Why:     "what the NSIS installer calls the product",
}, {
	Path:    "build/windows/wails.exe.manifest",
	Pattern: regexp.MustCompile(`name="io\.deffun\.xxvi" version="([^"]*)"`),
	Why:     "the assembly version inside the .exe's side-by-side manifest",
}, {
	Path:    "build/linux/nfpm/nfpm.yaml",
	Pattern: regexp.MustCompile(`(?m)^version: "([^"]*)"`),
	Why:     "the version of the deb/rpm/AUR packages",
}}

// build/windows/msix/app_manifest.xml deliberately does not appear above: the
// MSIX task points at a wails.json this project does not have, so that number
// is carried by nothing that ships.

// Read returns, per site, every version string found in it. root is the module
// root. A file that does not match its pattern is an error rather than an empty
// result: the pattern going stale is the same bug as the version going stale.
func Read(root string) (map[string][]string, error) {
	found := make(map[string][]string, len(Sites))
	for _, site := range Sites {
		path := filepath.Join(root, site.Path)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		matches := site.Pattern.FindAllStringSubmatch(string(data), -1)
		if len(matches) == 0 {
			return nil, fmt.Errorf("%s: версия не найдена — образец в internal/buildversion больше не подходит к этому файлу", site.Path)
		}
		versions := make([]string, 0, len(matches))
		for _, m := range matches {
			versions = append(versions, m[1])
		}
		found[site.Path] = versions
	}
	return found, nil
}

// Set writes version into every site, leaving the rest of each file byte for
// byte as it was. It returns the paths it changed.
func Set(root, version string) ([]string, error) {
	if !semverish.MatchString(version) {
		return nil, fmt.Errorf("%q — не версия вида x.y.z (без ведущего v: это дело тега)", version)
	}
	var changed []string
	for _, site := range Sites {
		path := filepath.Join(root, site.Path)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		next, n := replaceGroup(site.Pattern, string(data), version)
		if n == 0 {
			return nil, fmt.Errorf("%s: версия не найдена — образец в internal/buildversion больше не подходит к этому файлу", site.Path)
		}
		if next == string(data) {
			continue
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, []byte(next), info.Mode().Perm()); err != nil {
			return nil, err
		}
		changed = append(changed, site.Path)
	}
	return changed, nil
}

// semverish is deliberately looser than SemVer 2.0.0 and stricter than
// "anything": the updater compares under SemVer precedence, but this is the
// number a person types, and the only mistake worth catching here is the
// leading "v" that every tag has and no version string may.
var semverish = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)

// replaceGroup rewrites capturing group 1 of every match, keeping everything
// around it. Regexp.ReplaceAll cannot do this — its template refers to groups
// but writes the whole match — so the indices are walked by hand.
func replaceGroup(re *regexp.Regexp, s, with string) (string, int) {
	locs := re.FindAllStringSubmatchIndex(s, -1)
	if len(locs) == 0 {
		return s, 0
	}
	var out []byte
	last := 0
	for _, loc := range locs {
		start, end := loc[2], loc[3]
		out = append(out, s[last:start]...)
		out = append(out, with...)
		last = end
	}
	out = append(out, s[last:]...)
	return string(out), len(locs)
}
