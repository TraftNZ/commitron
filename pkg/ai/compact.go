package ai

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/johnstilia/commitron/pkg/tokenizer"
)

// Budget constants for deterministic diff compaction.
const (
	// defaultMaxDiffTokens bounds how much diff text reaches the model. Prompt
	// evaluation dominates wall time on self-hosted endpoints (measured ~240 tok/s),
	// so every token here costs latency directly. 12K tokens is roughly 1,200 changed
	// lines - far more than a commit message needs - while keeping prefill bounded.
	defaultMaxDiffTokens = 12000

	// elisionNoteTokens reserves room for the single per-file "... N of M changed lines
	// elided" marker, so a file never spends its whole budget on content and then
	// overflows on the note. One note per file (rather than per hunk) keeps the marker
	// overhead proportional to the number of files, not the number of hunks.
	elisionNoteTokens = 16
)

// compactHunk is one hunk of a file's diff, reduced to its enclosing-symbol trailer
// plus the changed lines themselves.
type compactHunk struct {
	symbol     string   // enclosing function/section from the @@ ... @@ trailer, may be empty
	lines      []string // changed lines, each still prefixed with '+' or '-'
	lineTokens []int    // token cost of each entry in lines, same order
	added      int
	removed    int
}

// compactFileDiff is a file's diff reduced to the parts that carry commit-message signal.
type compactFileDiff struct {
	path      string
	status    string
	added     int
	removed   int
	stubbed   bool // generated/binary/lockfile: rendered as a one-line note, never expanded
	stubNote  string
	hunks     []compactHunk
	headerTok int // token cost of the file header line
}

// CompactDiff renders a unified git diff into the smallest form that still carries
// commit-message signal, fitting within budgetTokens.
//
// The reduction is deterministic and requires no LLM round-trips: it drops diff
// plumbing (index/mode/---/+++ lines), unchanged context, and structurally empty
// lines, stubs generated files, and keeps every file header and hunk symbol. Only
// when the result still exceeds the budget does it elide changed lines, and it does
// so with an explicit marker so the model knows the view is partial. Every file and
// every hunk symbol always survives, so nothing disappears silently.
//
// The budget is honored for any budget that can hold at least one file path; below
// that the output is a single path, which is the smallest truthful answer available.
// Callers clamp to minDiffBudgetTokens, well above that floor.
func CompactDiff(diff string, budgetTokens int, tokenizerModel string) string {
	files := buildCompactFiles(diff, tokenizerModel)
	if len(files) == 0 {
		return diff
	}

	// Fixed cost: file headers and hunk symbols always survive, they are the skeleton
	// of the changeset and cost far less than the changed lines they label.
	fixed := 0
	demands := make([]int, len(files))
	for i := range files {
		fixed += files[i].headerTok + files[i].symbolTokens(tokenizerModel)
		if !files[i].stubbed {
			// Reserve the file's elision note up front so rendering can never overflow.
			fixed += elisionNoteTokens
		}
		demands[i] = files[i].contentTokens()
	}

	remaining := budgetTokens - fixed
	if remaining < 0 {
		// Even the skeleton does not fit; fall back to the leanest possible view.
		return renderSkeleton(files, budgetTokens, tokenizerModel)
	}

	shares := allocate(demands, remaining)

	var b strings.Builder
	for i := range files {
		files[i].render(&b, shares[i])
	}
	return strings.TrimSpace(b.String())
}

// buildCompactFiles parses the diff and strips every line that carries no
// commit-message signal, recording per-line token costs for later budgeting.
func buildCompactFiles(diff string, tokenizerModel string) []compactFileDiff {
	parsed := ParseDiffByFile(diff)
	files := make([]compactFileDiff, 0, len(parsed))

	for _, fd := range parsed {
		cf := compactFileDiff{
			path:    fd.Path,
			status:  fd.Status,
			added:   fd.Added,
			removed: fd.Removed,
		}

		switch {
		case isGenerated(fd.Path):
			cf.stubbed = true
			cf.stubNote = "generated/lockfile, contents omitted"
		case strings.Contains(fd.Content, "Binary files "):
			cf.stubbed = true
			cf.stubNote = "binary file"
		default:
			cf.hunks = splitHunks(fd.Content, tokenizerModel)
		}

		cf.headerTok = tokenizer.CountTokens(cf.header(), tokenizerModel)
		files = append(files, cf)
	}

	return files
}

// splitHunks breaks a single file's diff body into hunks, keeping only changed lines
// and the enclosing-symbol trailer of each @@ header.
func splitHunks(content string, tokenizerModel string) []compactHunk {
	var hunks []compactHunk
	var cur *compactHunk

	for _, line := range strings.Split(content, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			hunks = append(hunks, compactHunk{symbol: hunkSymbol(line)})
			cur = &hunks[len(hunks)-1]

		case isDiffPlumbing(line):
			// index/mode/rename/---/+++/diff --git lines duplicate the file header.

		case strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
			if cur == nil {
				hunks = append(hunks, compactHunk{})
				cur = &hunks[len(hunks)-1]
			}
			if strings.HasPrefix(line, "+") {
				cur.added++
			} else {
				cur.removed++
			}
			if isStructuralNoise(line) {
				// Lone braces, blank lines and bare delimiters cost tokens and say nothing.
				continue
			}
			cur.lines = append(cur.lines, line)
			cur.lineTokens = append(cur.lineTokens, tokenizer.CountTokens(line+"\n", tokenizerModel))
		}
		// Everything else (context lines starting with a space, blank separators) is dropped.
	}

	return hunks
}

// isGenerated checks if a file path matches known generated/lockfile/vendored patterns.
// Such files are stubbed to a one-line note: their diffs are enormous and carry almost
// no signal about the intent of a change.
func isGenerated(path string) bool {
	base := filepath.Base(path)
	switch base {
	case "go.sum", "go.work.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml",
		"Cargo.lock", "Gemfile.lock", "poetry.lock", "composer.lock", "Pipfile.lock",
		"flake.lock":
		return true
	}

	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, ".min.js") || strings.HasSuffix(lower, ".min.css") ||
		strings.HasSuffix(lower, ".map") {
		return true
	}

	// Vendored / third-party / generated directories anywhere in the path.
	for _, dir := range []string{"vendor", "third_party", "third-party", "node_modules",
		"dist", "build", "generated", "__generated__", ".next"} {
		if strings.Contains(path, "/"+dir+"/") || strings.HasPrefix(path, dir+"/") {
			return true
		}
	}

	if strings.HasSuffix(path, ".pb.go") || strings.HasSuffix(path, ".pb.gw.go") ||
		strings.HasSuffix(path, "_generated.go") || strings.HasSuffix(path, ".gen.go") ||
		strings.HasSuffix(path, ".g.dart") || strings.HasSuffix(path, ".freezed.dart") ||
		strings.HasSuffix(path, "_pb2.py") || strings.HasSuffix(path, "_pb2_grpc.py") {
		return true
	}

	return false
}

// isDiffPlumbing reports whether a line is git diff metadata whose content is already
// captured by the compact file header.
func isDiffPlumbing(line string) bool {
	for _, p := range []string{
		"diff --git", "index ", "--- ", "+++ ", "new file mode", "deleted file mode",
		"old mode", "new mode", "similarity index", "rename from", "rename to",
		"new mode ", "GIT binary patch",
	} {
		if strings.HasPrefix(line, p) {
			return true
		}
	}
	return line == "---" || line == "+++"
}

// isStructuralNoise reports whether a changed line is pure syntax scaffolding.
func isStructuralNoise(line string) bool {
	body := strings.TrimSpace(line[1:])
	switch body {
	case "", "{", "}", "(", ")", "[", "]", "})", "});", "};", ")};", "end", "*/", "/*":
		return true
	}
	return false
}

// hunkSymbol extracts the enclosing-symbol trailer from an @@ header, dropping the
// line-number ranges which carry no meaning for a commit message.
func hunkSymbol(line string) string {
	rest := line[2:]
	i := strings.Index(rest, "@@")
	if i < 0 {
		return ""
	}
	return strings.TrimSpace(rest[i+2:])
}

func (h compactHunk) symbolTokens(tokenizerModel string) int {
	if h.symbol == "" {
		return 0
	}
	return tokenizer.CountTokens("@ "+h.symbol+"\n", tokenizerModel)
}

func (h compactHunk) contentTokens() int {
	total := 0
	for _, t := range h.lineTokens {
		total += t
	}
	return total
}

func (f compactFileDiff) header() string {
	if f.stubbed {
		return fmt.Sprintf("## %s (%s, +%d/-%d) - %s\n", f.path, f.status, f.added, f.removed, f.stubNote)
	}
	return fmt.Sprintf("## %s (%s, +%d/-%d)\n", f.path, f.status, f.added, f.removed)
}

func (f compactFileDiff) contentTokens() int {
	if f.stubbed {
		return 0
	}
	total := 0
	for _, h := range f.hunks {
		total += h.contentTokens()
	}
	return total
}

// symbolTokens is the cost of the file's hunk symbols after collapsing runs of the
// same enclosing symbol, which is how render emits them.
func (f compactFileDiff) symbolTokens(tokenizerModel string) int {
	total := 0
	prev := ""
	for _, h := range f.hunks {
		if h.symbol == "" || h.symbol == prev {
			continue
		}
		total += h.symbolTokens(tokenizerModel)
		prev = h.symbol
	}
	return total
}

// render writes the file's compact form, spending at most budget tokens on changed
// lines. The header and every distinct hunk symbol are always written; whatever the
// budget could not fit is reported in a single explicit note.
func (f compactFileDiff) render(b *strings.Builder, budget int) {
	b.WriteString(f.header())
	if f.stubbed {
		b.WriteString("\n")
		return
	}

	demands := make([]int, len(f.hunks))
	totalLines := 0
	for i, h := range f.hunks {
		demands[i] = h.contentTokens()
		totalLines += len(h.lines)
	}
	shares := allocate(demands, budget)

	prevSymbol := ""
	written := 0
	for i, h := range f.hunks {
		// Consecutive hunks inside the same function repeat the same trailer; printing
		// it once keeps the output readable and avoids paying for the repetition.
		if h.symbol != "" && h.symbol != prevSymbol {
			b.WriteString("@ ")
			b.WriteString(h.symbol)
			b.WriteString("\n")
			prevSymbol = h.symbol
		}
		written += h.render(b, shares[i])
	}

	if written < totalLines {
		fmt.Fprintf(b, "... %d of %d changed lines elided\n", totalLines-written, totalLines)
	}
	b.WriteString("\n")
}

// render writes as many of the hunk's changed lines as budget allows, returning how
// many it wrote. The caller reports the shortfall once for the whole file.
func (h compactHunk) render(b *strings.Builder, budget int) int {
	spent := 0
	written := 0
	for i, line := range h.lines {
		cost := h.lineTokens[i]
		if spent+cost > budget {
			break
		}
		b.WriteString(line)
		b.WriteString("\n")
		spent += cost
		written++
	}
	return written
}

// renderSkeleton is the floor: when even file headers and hunk symbols exceed the
// budget, emit headers alone, then a bare path list, so no file is silently dropped.
func renderSkeleton(files []compactFileDiff, budget int, tokenizerModel string) string {
	var b strings.Builder
	for _, f := range files {
		b.WriteString(f.header())
	}
	out := strings.TrimSpace(b.String())
	if tokenizer.CountTokens(out, tokenizerModel) <= budget {
		return out
	}

	paths := make([]string, 0, len(files))
	for _, f := range files {
		paths = append(paths, f.path)
	}
	for len(paths) > 1 {
		out = "Files changed: " + strings.Join(paths, ", ")
		if tokenizer.CountTokens(out, tokenizerModel) <= budget {
			return out
		}
		paths = paths[:len(paths)-1]
	}
	return "Files changed: " + strings.Join(paths, ", ")
}

// allocate distributes total across demands using max-min fairness: no demand
// receives more than it asks for, and any surplus is redistributed evenly among the
// demands that are still short. This keeps a single huge file from starving the rest.
func allocate(demands []int, total int) []int {
	shares := make([]int, len(demands))
	if total <= 0 || len(demands) == 0 {
		return shares
	}

	// Process smallest demands first so surplus flows to the larger ones.
	order := make([]int, len(demands))
	for i := range order {
		order[i] = i
	}
	for i := 1; i < len(order); i++ {
		for j := i; j > 0 && demands[order[j]] < demands[order[j-1]]; j-- {
			order[j], order[j-1] = order[j-1], order[j]
		}
	}

	remaining := total
	left := len(order)
	for _, idx := range order {
		fair := remaining / left
		if demands[idx] <= fair {
			shares[idx] = demands[idx]
		} else {
			shares[idx] = fair
		}
		remaining -= shares[idx]
		left--
	}

	return shares
}
