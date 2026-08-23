package buildversion

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// Set has to leave every file it touches exactly as it was apart from the
// number, because these files are the packaging: a dropped line in Info.plist
// is a bundle macOS refuses to open, and nothing between here and a user would
// notice.
func TestSetChangesTheNumberAndNothingElse(t *testing.T) {
	root := t.TempDir()
	plist := filepath.Join("build", "darwin", "Info.plist")
	if err := os.MkdirAll(filepath.Join(root, "build", "darwin"), 0o755); err != nil {
		t.Fatal(err)
	}
	before := "<key>CFBundleVersion</key>\n\t<string>0.1.0</string>\n<key>CFBundleShortVersionString</key>\n\t<string>0.1.0</string>\n<key>CFBundleName</key>\n\t<string>XXVI</string>\n"
	write(t, root, "version.go", `const appVersion = "0.1.0"`+"\n")
	write(t, root, plist, before)

	only := []Site{
		{Path: "version.go", Pattern: regexp.MustCompile(`const appVersion = "([^"]*)"`)},
		{Path: plist, Pattern: regexp.MustCompile(`(?s)<key>CFBundle(?:ShortVersionString|Version)</key>\s*<string>([^<]*)</string>`)},
	}
	restore := Sites
	Sites = only
	t.Cleanup(func() { Sites = restore })

	changed, err := Set(root, "0.2.0")
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(changed) != 2 {
		t.Fatalf("изменено %v, ждали оба файла", changed)
	}
	// Both numbers in the same file, and the name between them untouched.
	want := "<key>CFBundleVersion</key>\n\t<string>0.2.0</string>\n<key>CFBundleShortVersionString</key>\n\t<string>0.2.0</string>\n<key>CFBundleName</key>\n\t<string>XXVI</string>\n"
	if got := read(t, root, plist); got != want {
		t.Errorf("Info.plist после Set:\n%q\nждали:\n%q", got, want)
	}

	// And the same version again is not a change: a release that is already at
	// the number has nothing to commit.
	changed, err = Set(root, "0.2.0")
	if err != nil {
		t.Fatalf("Set повторно: %v", err)
	}
	if len(changed) != 0 {
		t.Errorf("повторный Set изменил %v", changed)
	}
}

// The leading "v" is the tag's, never the version's — and it is the one typo
// worth catching, because a build that calls itself "v0.2.0" compares unequal
// to every release for ever.
func TestSetRefusesATagInsteadOfAVersion(t *testing.T) {
	for _, bad := range []string{"v0.2.0", "0.2", "latest", ""} {
		if _, err := Set(t.TempDir(), bad); err == nil {
			t.Errorf("Set(%q) прошло, а не должно было", bad)
		}
	}
}

// A pattern that no longer matches its file is the same failure as a version
// that no longer matches the others: silent until a release goes out. Read says
// so instead of answering with nothing.
func TestReadRefusesAFileThePatternNoLongerMatches(t *testing.T) {
	root := t.TempDir()
	write(t, root, "version.go", "const appVersion = 0.1.0\n")
	restore := Sites
	Sites = []Site{{Path: "version.go", Pattern: regexp.MustCompile(`const appVersion = "([^"]*)"`)}}
	t.Cleanup(func() { Sites = restore })

	if _, err := Read(root); err == nil {
		t.Fatal("Read прошёл по файлу, в котором версии нет")
	}
}

func write(t *testing.T, root, path, body string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
