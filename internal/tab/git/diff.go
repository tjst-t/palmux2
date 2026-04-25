package git

import (
	"strings"
)

// DiffFile represents one file's worth of unified diff.
type DiffFile struct {
	OldPath string      `json:"oldPath"`
	NewPath string      `json:"newPath"`
	Header  string      `json:"header"` // raw diff --git ... + --- / +++ headers
	Hunks   []DiffHunk  `json:"hunks"`
	IsBinary bool       `json:"isBinary,omitempty"`
}

// DiffHunk represents one @@ ... @@ section.
type DiffHunk struct {
	Header string     `json:"header"` // the @@ line
	Lines  []DiffLine `json:"lines"`
	OldStart int      `json:"oldStart"`
	OldCount int      `json:"oldCount"`
	NewStart int      `json:"newStart"`
	NewCount int      `json:"newCount"`
}

// DiffLine represents one line inside a hunk.
type DiffLine struct {
	Kind string `json:"kind"` // "context" | "add" | "del" | "meta"
	Text string `json:"text"` // line without the leading marker
}

// ParseUnifiedDiff turns `git diff` output into a structured form. Files
// without changes (binary, mode-only) get IsBinary=true and no hunks.
func ParseUnifiedDiff(s string) []DiffFile {
	if s == "" {
		return nil
	}
	var files []DiffFile
	var cur *DiffFile
	var curHunk *DiffHunk
	flushHunk := func() {
		if curHunk == nil || cur == nil {
			return
		}
		cur.Hunks = append(cur.Hunks, *curHunk)
		curHunk = nil
	}
	flushFile := func() {
		flushHunk()
		if cur == nil {
			return
		}
		files = append(files, *cur)
		cur = nil
	}

	for _, line := range strings.Split(s, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushFile()
			oldP, newP := paths(line)
			cur = &DiffFile{Header: line + "\n", OldPath: oldP, NewPath: newP}
		case strings.HasPrefix(line, "Binary files "):
			if cur != nil {
				cur.IsBinary = true
				cur.Header += line + "\n"
			}
		case strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ "):
			if cur != nil {
				cur.Header += line + "\n"
			}
		case strings.HasPrefix(line, "@@"):
			flushHunk()
			h := DiffHunk{Header: line}
			h.OldStart, h.OldCount, h.NewStart, h.NewCount = parseHunkRange(line)
			curHunk = &h
		case strings.HasPrefix(line, "+") && curHunk != nil && !strings.HasPrefix(line, "+++"):
			curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: "add", Text: line[1:]})
		case strings.HasPrefix(line, "-") && curHunk != nil && !strings.HasPrefix(line, "---"):
			curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: "del", Text: line[1:]})
		case strings.HasPrefix(line, " ") && curHunk != nil:
			curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: "context", Text: line[1:]})
		case strings.HasPrefix(line, "\\"):
			if curHunk != nil {
				curHunk.Lines = append(curHunk.Lines, DiffLine{Kind: "meta", Text: line})
			}
		case line == "":
			// blank between hunks/files; ignore
		default:
			// metadata lines like `index ...`, `new file mode`, `similarity index` etc.
			if cur != nil && curHunk == nil {
				cur.Header += line + "\n"
			}
		}
	}
	flushFile()
	return files
}

func paths(line string) (oldPath, newPath string) {
	// "diff --git a/<old> b/<new>"
	rest := strings.TrimPrefix(line, "diff --git ")
	parts := strings.SplitN(rest, " ", 2)
	if len(parts) != 2 {
		return "", ""
	}
	oldPath = strings.TrimPrefix(parts[0], "a/")
	newPath = strings.TrimPrefix(parts[1], "b/")
	return
}

// parseHunkRange parses `@@ -10,5 +12,7 @@ context` into the four numbers.
func parseHunkRange(line string) (oldStart, oldCount, newStart, newCount int) {
	if !strings.HasPrefix(line, "@@") {
		return
	}
	// Strip the trailing "@@" and any context after it.
	rest := strings.TrimPrefix(line, "@@")
	if idx := strings.Index(rest, "@@"); idx >= 0 {
		rest = rest[:idx]
	}
	rest = strings.TrimSpace(rest)
	parts := strings.Fields(rest)
	for _, p := range parts {
		if strings.HasPrefix(p, "-") {
			oldStart, oldCount = parseRange(strings.TrimPrefix(p, "-"))
		} else if strings.HasPrefix(p, "+") {
			newStart, newCount = parseRange(strings.TrimPrefix(p, "+"))
		}
	}
	return
}

func parseRange(s string) (start, count int) {
	count = 1
	if idx := strings.Index(s, ","); idx >= 0 {
		start = atoi(s[:idx])
		count = atoi(s[idx+1:])
	} else {
		start = atoi(s)
	}
	return
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			break
		}
		n = n*10 + int(c-'0')
	}
	return n
}

// BuildHunkPatch reconstructs a minimal patch containing one hunk so it can
// be fed to `git apply` for stage/discard operations. The headers are taken
// from file.Header and the @@/lines from the chosen hunk.
func BuildHunkPatch(file DiffFile, hunk DiffHunk) string {
	var sb strings.Builder
	// file header (already ends with \n)
	sb.WriteString(file.Header)
	sb.WriteString(hunk.Header)
	sb.WriteString("\n")
	for _, l := range hunk.Lines {
		switch l.Kind {
		case "add":
			sb.WriteByte('+')
		case "del":
			sb.WriteByte('-')
		case "context":
			sb.WriteByte(' ')
		case "meta":
			sb.WriteString(l.Text)
			sb.WriteByte('\n')
			continue
		}
		sb.WriteString(l.Text)
		sb.WriteByte('\n')
	}
	return sb.String()
}
