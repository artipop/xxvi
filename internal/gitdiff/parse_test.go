package gitdiff

import "testing"

const modified = `diff --git a/internal/app/api.go b/internal/app/api.go
index 1111111..2222222 100644
--- a/internal/app/api.go
+++ b/internal/app/api.go
@@ -10,7 +10,8 @@ func (s *API) Card(id string) (CardView, error) {
 	one := 1
 	two := 2
-	three := 3
+	three := 33
+	four := 4
 	five := 5
 	six := 6
`

func TestParseCountsAndNumbersLines(t *testing.T) {
	files, truncated := Parse(modified, 0)
	if truncated {
		t.Fatal("нечего было резать, а парсер сказал, что резал")
	}
	if len(files) != 1 {
		t.Fatalf("файлов %d, ждали 1", len(files))
	}
	f := files[0]
	if f.Path != "internal/app/api.go" || f.Status != StatusModified {
		t.Fatalf("файл разобран как %+v", f)
	}
	if f.Added != 2 || f.Removed != 1 {
		t.Fatalf("+%d −%d, ждали +2 −1", f.Added, f.Removed)
	}
	if len(f.Hunks) != 1 {
		t.Fatalf("кусков %d, ждали 1", len(f.Hunks))
	}
	h := f.Hunks[0]
	if h.Heading != "func (s *API) Card(id string) (CardView, error) {" {
		t.Fatalf("заголовок куска: %q", h.Heading)
	}
	want := []Line{
		{Kind: LineContext, Text: "\tone := 1", Old: 10, New: 10},
		{Kind: LineContext, Text: "\ttwo := 2", Old: 11, New: 11},
		{Kind: LineDel, Text: "\tthree := 3", Old: 12},
		{Kind: LineAdd, Text: "\tthree := 33", New: 12},
		{Kind: LineAdd, Text: "\tfour := 4", New: 13},
		{Kind: LineContext, Text: "\tfive := 5", Old: 13, New: 14},
		{Kind: LineContext, Text: "\tsix := 6", Old: 14, New: 15},
	}
	if len(h.Lines) != len(want) {
		t.Fatalf("строк %d, ждали %d: %+v", len(h.Lines), len(want), h.Lines)
	}
	for i, w := range want {
		if h.Lines[i] != w {
			t.Fatalf("строка %d: %+v, ждали %+v", i, h.Lines[i], w)
		}
	}
}

func TestParseReadsWhatKindOfChangeItIs(t *testing.T) {
	patch := `diff --git a/новый.txt b/новый.txt
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/новый.txt
@@ -0,0 +1,2 @@
+первая
+вторая
diff --git a/старый.txt b/старый.txt
deleted file mode 100644
index 4444444..0000000
--- a/старый.txt
+++ /dev/null
@@ -1 +0,0 @@
-был да сплыл
diff --git a/был.go b/стал.go
similarity index 98%
rename from был.go
rename to стал.go
index 5555555..6666666 100644
--- a/был.go
+++ b/стал.go
@@ -1,2 +1,2 @@
 package main
-// старое
+// новое
diff --git a/logo.png b/logo.png
index 7777777..8888888 100644
Binary files a/logo.png and b/logo.png differ
`
	files, _ := Parse(patch, 0)
	if len(files) != 4 {
		t.Fatalf("файлов %d, ждали 4", len(files))
	}
	if f := files[0]; f.Status != StatusAdded || f.Path != "новый.txt" || f.Added != 2 {
		t.Fatalf("заведённый файл: %+v", f)
	}
	if f := files[1]; f.Status != StatusDeleted || f.Path != "старый.txt" || f.Removed != 1 {
		t.Fatalf("удалённый файл: %+v", f)
	}
	if f := files[2]; f.Status != StatusRenamed || f.OldPath != "был.go" || f.Path != "стал.go" {
		t.Fatalf("переименованный файл: %+v", f)
	}
	if f := files[3]; !f.Binary || len(f.Hunks) != 0 {
		t.Fatalf("двоичный файл: %+v", f)
	}
}

// A name with a space in it has no unambiguous place to split «diff --git»,
// which is why the two paths are read from the ±±± lines that follow.
func TestParseReadsPathWithSpace(t *testing.T) {
	patch := `diff --git a/папка/два слова.md b/папка/два слова.md
index 1111111..2222222 100644
--- a/папка/два слова.md
+++ b/папка/два слова.md
@@ -1 +1 @@
-было
+стало
`
	files, _ := Parse(patch, 0)
	if len(files) != 1 || files[0].Path != "папка/два слова.md" {
		t.Fatalf("путь с пробелом: %+v", files)
	}
}

// git quotes a path only when it has to — core.quotePath is off, so a Russian
// name arrives as itself and a quote in a name arrives escaped.
func TestParseReadsQuotedPath(t *testing.T) {
	patch := `diff --git "a/стран\"ный.txt" "b/стран\"ный.txt"
index 1111111..2222222 100644
--- "a/стран\"ный.txt"
+++ "b/стран\"ный.txt"
@@ -1 +1 @@
-было
+стало
`
	files, _ := Parse(patch, 0)
	if len(files) != 1 || files[0].Path != `стран"ный.txt` {
		t.Fatalf("путь в кавычках: %+v", files)
	}
}

// The budget stops lines from being kept, not from being counted: a truncated
// diff still says how big it was, which is the number the person decides by.
func TestParseBudgetKeepsCountsTrue(t *testing.T) {
	files, truncated := Parse(modified, 3)
	if !truncated {
		t.Fatal("резали, а парсер не сказал")
	}
	f := files[0]
	if len(f.Hunks[0].Lines) != 3 {
		t.Fatalf("оставлено строк %d, ждали 3", len(f.Hunks[0].Lines))
	}
	if f.Added != 2 || f.Removed != 1 {
		t.Fatalf("+%d −%d, ждали +2 −1", f.Added, f.Removed)
	}
}

func TestParseNoteBelongsToNeitherSide(t *testing.T) {
	patch := `diff --git a/a.txt b/a.txt
index 1111111..2222222 100644
--- a/a.txt
+++ b/a.txt
@@ -1 +1 @@
-было
\ No newline at end of file
+стало
`
	files, _ := Parse(patch, 0)
	lines := files[0].Hunks[0].Lines
	if len(lines) != 3 || lines[1].Kind != LineNote || lines[1].Old != 0 || lines[1].New != 0 {
		t.Fatalf("строки: %+v", lines)
	}
	if files[0].Added != 1 || files[0].Removed != 1 {
		t.Fatalf("заметка сосчиталась как изменение: %+v", files[0])
	}
}

func TestParseEmptyPatchIsNoFiles(t *testing.T) {
	files, truncated := Parse("", 0)
	if len(files) != 0 || truncated {
		t.Fatalf("пустой патч разобрался в %+v", files)
	}
}
