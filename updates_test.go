package main

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// The framework keeps none of this: the download is a temporary directory the
// helper deletes, and the version a person skipped lives in a field of the
// running Updater. So the file is the whole of «пропустить» meaning more than
// «до перезапуска», and it is what this checks.
func TestWhatSurvivesARestartIsWrittenAndReadBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updates.json")

	off := false
	want := updateSettings{Enabled: &off, SkippedVersion: "1.2.3", LastCheckedAt: "2026-08-23T09:00:00Z"}
	if err := writeUpdateSettings(path, want); err != nil {
		t.Fatalf("запись: %v", err)
	}

	got := readUpdateSettings(path, quiet())
	if got.Enabled == nil || *got.Enabled {
		t.Errorf("выключенная проверка вернулась как %v", got.Enabled)
	}
	if got.SkippedVersion != want.SkippedVersion || got.LastCheckedAt != want.LastCheckedAt {
		t.Errorf("прочитали %+v, писали %+v", got, want)
	}
}

// A file written before `enabled` existed has not answered the question, and
// «не отвечали» is «да»: an application that quietly stopped looking for
// updates because of a field it never had is one that stops updating.
func TestAFileThatNeverAnsweredMeansTheCheckIsOn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updates.json")
	if err := os.WriteFile(path, []byte(`{"skippedVersion":"1.2.3"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := readUpdateSettings(path, quiet())
	if s.Enabled != nil {
		t.Fatalf("Enabled = %v, ждали пустое", *s.Enabled)
	}
	if !s.enabled() {
		t.Error("непрочитанный ответ читается как «выключено»")
	}

	// And an explicit no stays a no.
	off := false
	if (updateSettings{Enabled: &off}).enabled() {
		t.Error("выключенная проверка читается как включённая")
	}
}

// Neither a missing file nor a broken one may stop the application: refusing to
// start because a preferences file is malformed is a worse answer than checking
// for updates one more time than asked.
func TestABrokenOrMissingFileIsTheDefaults(t *testing.T) {
	dir := t.TempDir()

	if s := readUpdateSettings(filepath.Join(dir, "нет.json"), quiet()); !s.enabled() || s.SkippedVersion != "" {
		t.Errorf("отсутствующий файл дал %+v", s)
	}

	broken := filepath.Join(dir, "updates.json")
	if err := os.WriteFile(broken, []byte("{это не json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if s := readUpdateSettings(broken, quiet()); !s.enabled() || s.SkippedVersion != "" {
		t.Errorf("испорченный файл дал %+v", s)
	}
}

// The file is occasionally read by a person, so it is written the way the rest
// of this application writes what a person may open: whole, indented, and with
// the fields nobody answered left out rather than written as zeroes.
func TestTheFileIsWrittenToBeRead(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updates.json")
	if err := writeUpdateSettings(path, updateSettings{SkippedVersion: "1.2.3"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("записанный файл не читается как JSON: %v", err)
	}
	if _, ok := raw["enabled"]; ok {
		t.Error("неотвеченный вопрос записан как ответ")
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Error("файл без перевода строки в конце")
	}
}
