// Package gitdiff reads what changed in a folder and turns it into something a
// screen can draw: files, hunks and lines, with the numbers already counted.
//
// The patch comes from git itself rather than from a comparison of our own.
// The person looking at the ribbon and the agent working in the same folder use
// that git — its renames, its whitespace rules, its .gitattributes — and a
// second opinion about what changed would be a second answer to a question that
// already has one.
//
// Parsing is here and running git is next door: the parser is a pure function
// over text, which is the part worth testing without a repository.
package gitdiff

import (
	"strconv"
	"strings"
)

// Line kinds. A line is context, an addition, a removal, or a note git left
// inside the hunk («\ No newline at end of file») that belongs to neither side.
const (
	LineContext = "ctx"
	LineAdd     = "add"
	LineDel     = "del"
	LineNote    = "note"
)

// File statuses, as the patch declares them.
const (
	StatusAdded    = "added"
	StatusDeleted  = "deleted"
	StatusModified = "modified"
	StatusRenamed  = "renamed"
)

// Line is one row of a hunk. Old and New are the line numbers on each side, and
// zero means the line is not on that side — which is what lets the viewer show
// two columns of numbers without counting them again.
type Line struct {
	Kind string `json:"kind"`
	Text string `json:"text"`
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
}

// Hunk is one changed region. Heading is what git puts after the @@ — usually
// the enclosing function — and it is kept because it is the only thing telling
// a reader where in the file they are.
type Hunk struct {
	Header  string `json:"header"`
	Heading string `json:"heading,omitempty"`
	Lines   []Line `json:"lines"`
}

// File is one file in the patch.
type File struct {
	Path string `json:"path"`
	// OldPath is set only for a rename: elsewhere it would repeat Path.
	OldPath string `json:"oldPath,omitempty"`
	Status  string `json:"status"`
	// Binary files have no hunks and never will: git says they differ and
	// stops, and so do we — a screen that renders bytes as text is a screen
	// nobody can read.
	Binary  bool   `json:"binary,omitempty"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Hunks   []Hunk `json:"hunks,omitempty"`
}

// Parse turns a unified patch into files. budget caps how many hunk lines are
// kept in total; the parser stops adding them once it is spent and reports that
// it did. Zero or less means no cap.
//
// A diff too large to draw is a real answer — an agent that rewrote a lockfile
// produces one — and it has to arrive as "here is the beginning, and there is
// more" rather than as a window that never opens.
func Parse(patch string, budget int) (files []File, truncated bool) {
	var (
		cur      *File
		hunk     *Hunk
		oldNo    int
		newNo    int
		kept     int
		overflow bool
	)

	flushHunk := func() {
		if cur != nil && hunk != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
		}
		hunk = nil
	}
	flushFile := func() {
		flushHunk()
		if cur != nil {
			files = append(files, *cur)
		}
		cur = nil
	}

	// The patch ends with a newline, and the empty string after it is not a
	// line of anything: taken as one, every file would gain a phantom context
	// row at its end.
	for _, line := range strings.Split(strings.TrimSuffix(patch, "\n"), "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushFile()
			oldPath, newPath := gitHeaderPaths(line)
			cur = &File{Path: newPath, OldPath: oldPath, Status: StatusModified}
			continue
		}
		if cur == nil {
			// Anything before the first «diff --git» is not ours to read.
			continue
		}

		switch {
		case strings.HasPrefix(line, "@@"):
			flushHunk()
			header, heading := splitHunkHeader(line)
			oldNo, newNo = hunkStarts(header)
			hunk = &Hunk{Header: header, Heading: heading}
			continue
		case hunk == nil:
			// Still in the file's header: everything that says what kind of
			// change this is says it here.
			switch {
			case strings.HasPrefix(line, "new file mode"):
				cur.Status = StatusAdded
			case strings.HasPrefix(line, "deleted file mode"):
				cur.Status = StatusDeleted
			case strings.HasPrefix(line, "rename from "):
				cur.Status = StatusRenamed
				cur.OldPath = unquote(strings.TrimPrefix(line, "rename from "))
			case strings.HasPrefix(line, "rename to "):
				cur.Status = StatusRenamed
				cur.Path = unquote(strings.TrimPrefix(line, "rename to "))
			case strings.HasPrefix(line, "--- "):
				if path, ok := sidePath(line, "--- "); ok {
					cur.OldPath = path
				} else {
					cur.Status = StatusAdded
				}
			case strings.HasPrefix(line, "+++ "):
				if path, ok := sidePath(line, "+++ "); ok {
					cur.Path = path
				} else {
					cur.Status = StatusDeleted
				}
			case strings.HasPrefix(line, "Binary files "), strings.HasPrefix(line, "GIT binary patch"):
				cur.Binary = true
			}
			continue
		}

		// Inside a hunk. An empty line is a context line whose space git left
		// off — some producers do, and dropping it would shift every number
		// under it.
		kind, text := LineContext, ""
		if line != "" {
			switch line[0] {
			case '+':
				kind, text = LineAdd, line[1:]
			case '-':
				kind, text = LineDel, line[1:]
			case ' ':
				text = line[1:]
			case '\\':
				kind, text = LineNote, strings.TrimSpace(line[1:])
			default:
				// Not a hunk line at all: the patch has moved on to something
				// we do not read, so neither do we.
				flushHunk()
				continue
			}
		}

		row := Line{Kind: kind, Text: text}
		switch kind {
		case LineAdd:
			cur.Added++
			row.New, newNo = newNo, newNo+1
		case LineDel:
			cur.Removed++
			row.Old, oldNo = oldNo, oldNo+1
		case LineContext:
			row.Old, oldNo = oldNo, oldNo+1
			row.New, newNo = newNo, newNo+1
		}
		if budget > 0 && kept >= budget {
			overflow = true
			continue
		}
		kept++
		hunk.Lines = append(hunk.Lines, row)
	}
	flushFile()
	return files, overflow
}

// gitHeaderPaths reads the two paths out of «diff --git a/x b/y».
//
// The line is split from the right at « b/», because a path may contain the
// separator and the header gives no other way to tell: «a/b b/c b/b b/c» is a
// real line for a file called «b b/c». A quoted path is unambiguous and read as
// such.
func gitHeaderPaths(line string) (oldPath, newPath string) {
	rest := strings.TrimPrefix(line, "diff --git ")
	if strings.HasPrefix(rest, `"`) {
		if a, tail, ok := cutQuoted(rest); ok {
			return trimPrefixSide(a), trimPrefixSide(unquoteLeading(strings.TrimSpace(tail)))
		}
	}
	if at := strings.LastIndex(rest, " b/"); at > 0 {
		return trimPrefixSide(rest[:at]), trimPrefixSide(rest[at+1:])
	}
	return "", ""
}

// sidePath reads «--- a/x» or «+++ b/x». The false is /dev/null: the side does
// not exist, which is how a patch says added or deleted.
func sidePath(line, prefix string) (string, bool) {
	rest := strings.TrimSpace(strings.TrimPrefix(line, prefix))
	// git appends a timestamp in some formats; ours does not, and a tab is the
	// documented separator when it does.
	if at := strings.IndexByte(rest, '\t'); at >= 0 {
		rest = rest[:at]
	}
	if rest == "/dev/null" {
		return "", false
	}
	return trimPrefixSide(unquote(rest)), true
}

// trimPrefixSide drops the a/ or b/ git puts in front of a path.
func trimPrefixSide(path string) string {
	path = unquote(path)
	if len(path) > 2 && (strings.HasPrefix(path, "a/") || strings.HasPrefix(path, "b/")) {
		return path[2:]
	}
	return path
}

// splitHunkHeader separates «@@ -1,2 +3,4 @@» from the heading after it.
func splitHunkHeader(line string) (header, heading string) {
	if at := strings.Index(line[2:], "@@"); at >= 0 {
		end := at + 4
		return strings.TrimSpace(line[:end]), strings.TrimSpace(line[end:])
	}
	return strings.TrimSpace(line), ""
}

// hunkStarts reads the first line number of each side out of a hunk header.
func hunkStarts(header string) (oldStart, newStart int) {
	for _, part := range strings.Fields(header) {
		if len(part) < 2 {
			continue
		}
		switch part[0] {
		case '-':
			oldStart = leadingNumber(part[1:])
		case '+':
			newStart = leadingNumber(part[1:])
		}
	}
	if oldStart == 0 {
		oldStart = 1
	}
	if newStart == 0 {
		newStart = 1
	}
	return oldStart, newStart
}

func leadingNumber(s string) int {
	if at := strings.IndexByte(s, ','); at >= 0 {
		s = s[:at]
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// unquote reads git's C-style quoting, which it uses for a path with a quote, a
// tab or a newline in it. Everything else arrives as itself, because git runs
// with core.quotePath=false — a Russian filename is a filename, not a row of
// escapes.
func unquote(s string) string {
	if !strings.HasPrefix(s, `"`) {
		return s
	}
	if out, err := strconv.Unquote(s); err == nil {
		return out
	}
	return s
}

// unquoteLeading unquotes a value that may be followed by nothing else.
func unquoteLeading(s string) string {
	if out, _, ok := cutQuoted(s); ok {
		return out
	}
	return s
}

// cutQuoted takes one C-quoted value off the front of s and returns the rest.
func cutQuoted(s string) (value, rest string, ok bool) {
	if !strings.HasPrefix(s, `"`) {
		return "", s, false
	}
	for i := 1; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			out, err := strconv.Unquote(s[:i+1])
			if err != nil {
				return "", s, false
			}
			return out, s[i+1:], true
		}
	}
	return "", s, false
}
