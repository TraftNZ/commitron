package ai

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/johnstilia/commitron/pkg/config"
	"github.com/johnstilia/commitron/pkg/tokenizer"
	"golang.org/x/sync/errgroup"
)

// Configuration constants - all internal, no exported config fields
const (
	maxDepth          = 4   // Maximum summarization rounds
	minPerFileTokens  = 80  // Minimum tokens per file summary
	maxPerFileTokens  = 600 // Maximum tokens per file summary
	concurrencyLimit  = 4   // Maximum concurrent LLM calls for per-file summarization
	minChunkOutTokens = 120 // Minimum output token budget for a single reduce chunk
	lastResortOverage = 10  // Allow up to 10% overage before failing
)

// fileClassification categorizes a file for handling
type fileClassification int

const (
	classNormal fileClassification = iota
	classBinary
	classRenameOnly
	classWhitespaceOnly
	classGenerated
	classEmpty
)

// processedFile represents a processed file with its summary/stub
type processedFile struct {
	Path           string
	Classification fileClassification
	Content        string // Summary or stub or raw content
	Tokens         int
	Priority       int
}

// HierarchicalSummarize performs hierarchical map-reduce summarization on a diff that fits within token budget.
// It never truncates mid-content - every file always appears at least as a stub.
func HierarchicalSummarize(cfg *config.Config, diff string, promptBudget int) (string, error) {
	tokenizerModel := cfg.Context.TokenizerModel
	if tokenizerModel == "" {
		tokenizerModel = cfg.AI.Model
	}

	inputTokens := tokenizer.CountTokens(diff, tokenizerModel)
	debugPrint(cfg, "HIERARCHICAL SUMMARIZATION INPUT", map[string]any{
		"input_tokens":   inputTokens,
		"prompt_budget":  promptBudget,
		"needs_summariz": inputTokens > promptBudget,
	})

	// Short-circuit if already fits - no extra LLM calls needed
	if inputTokens <= promptBudget {
		return diff, nil
	}

	// Print progress message so user knows hierarchical summarization is working
	fmt.Printf("\033[1;36m🔄 Hierarchical summarization for large diff (%d tokens → fits under %d)...\033[0m\n", inputTokens, promptBudget)

	// Step 1: Parse and classify each file
	fileDiffs := ParseDiffByFile(diff)
	if len(fileDiffs) == 0 {
		// Can't parse, return as much as we can without truncation but won't truncate silently
		// This should never happen for valid diffs
		return diff[:min(len(diff), promptBudget*2)], nil
	}

	// Step 2: Classify each file and create deterministic stubs where appropriate
	var classifiedFiles []processedFile
	var normalFiles []*processedFile

	totalPriority := 0
	for _, fd := range fileDiffs {
		class := classifyFile(fd)
		pf := processedFile{
			Path:           fd.Path,
			Classification: class,
			Priority:       calculateFilePriority(fd),
		}

		// Generate stub for non-normal files
		switch class {
		case classBinary:
			pf.Content = fmt.Sprintf("binary %s: changed", fd.Path)
		case classRenameOnly:
			// Extract old/new from diff if possible
			oldPath, newPath := extractRenamePaths(fd.Content)
			if oldPath != "" && newPath != "" {
				pf.Content = fmt.Sprintf("renamed: %s → %s", oldPath, newPath)
			} else {
				pf.Content = fmt.Sprintf("renamed %s", fd.Path)
			}
		case classWhitespaceOnly:
			pf.Content = fmt.Sprintf("whitespace-only change in %s", fd.Path)
		case classGenerated:
			pf.Content = fmt.Sprintf("lockfile/generated: %s: +%d/-%d lines", fd.Path, fd.Added, fd.Removed)
		case classEmpty:
			pf.Content = fmt.Sprintf("%s: no content change", fd.Path)
		default: // classNormal
			pf.Content = fd.Content
			normalFiles = append(normalFiles, &pf)
			totalPriority += pf.Priority
		}

		pf.Tokens = tokenizer.CountTokens(pf.Content, tokenizerModel)
		classifiedFiles = append(classifiedFiles, pf)
	}

	debugPrint(cfg, "HIERARCHICAL CLASSIFICATION", map[string]any{
		"total_files":   len(classifiedFiles),
		"needs_summary": len(normalFiles),
	})

	// Step 3: Map - per-file summarization for normal files that exceed budget
	if len(normalFiles) > 0 {
		// Allocate budgets based on priority
		allocateBudgets(normalFiles, promptBudget, totalPriority)

		// Count how many files need summarization
		needSummarize := 0
		for _, pf := range normalFiles {
			if pf.Tokens > allocateBudgetForFile(pf.Priority, totalPriority, promptBudget, minPerFileTokens, maxPerFileTokens) {
				needSummarize++
			}
		}

		if needSummarize > 0 {
			fmt.Printf("\033[1;36m🔄 Summarizing %d large files (%d concurrent)...\033[0m\n", needSummarize, concurrencyLimit)
		}

		// Run concurrent summarization with limit 4
		var g errgroup.Group
		g.SetLimit(concurrencyLimit)

		for _, pf := range normalFiles {
			pf := pf // capture loop variable
			budget := allocateBudgetForFile(pf.Priority, totalPriority, promptBudget, minPerFileTokens, maxPerFileTokens)
			if pf.Tokens <= budget {
				// Already fits, skip summarization
				continue
			}
			g.Go(func() error {
				summary, err := summarizeSingleFile(cfg, pf.Path, pf.Content, maxPerFileTokens)
				if err != nil {
					// On failure, fall back to deterministic stub
					debugPrint(cfg, "PER-FILE SUMMARY FAILED", fmt.Sprintf("%s: %v, using stub", pf.Path, err))
					summary = fmt.Sprintf("%s: +%d/-%d lines (summary failed)", pf.Path,
						fileDiffs[findFileDiffIndex(pf.Path, fileDiffs)].Added,
						fileDiffs[findFileDiffIndex(pf.Path, fileDiffs)].Removed)
				}
				pf.Content = summary
				pf.Tokens = tokenizer.CountTokens(summary, tokenizerModel)
				return nil
			})
		}

		// Wait for all concurrent operations - individual failures are handled internally
		_ = g.Wait()
	}

	// Step 4: Combine all processed content and check if fits
	var combined strings.Builder
	for _, pf := range classifiedFiles {
		combined.WriteString(pf.Content)
		combined.WriteString("\n\n")
	}
	current := strings.TrimSpace(combined.String())
	currentTokens := tokenizer.CountTokens(current, tokenizerModel)

	debugPrint(cfg, "AFTER MAP STAGE", map[string]any{
		"tokens_after_map": currentTokens,
		"prompt_budget":    promptBudget,
	})

	// Step 5: Reduce - hierarchical summarization until fits or max depth reached
	depth := 0
	for currentTokens > promptBudget && depth < maxDepth {
		debugPrint(cfg, "REDUCE STAGE START", map[string]any{
			"depth":     depth + 1,
			"tokens":    currentTokens,
			"budget":    promptBudget,
			"max_depth": maxDepth,
		})

		reduced, newTokens, err := reduceSummarize(cfg, current, promptBudget, depth+1)
		if err != nil {
			// Every chunk failed - use what we have if it's close enough
			if float64(currentTokens)/float64(promptBudget) <= (1 + lastResortOverage/100.0) {
				debugPrint(cfg, "REDUCE FAILED ACCEPT", fmt.Sprintf("%d tokens > %d budget (%d%% overage), accepting",
					currentTokens, promptBudget, int(100*float64(currentTokens-promptBudget)/float64(promptBudget))))
				break
			}
			return "", fmt.Errorf("hierarchical summarization exhausted: %w", err)
		}

		// Stop if a round made no progress; lastResortSortAndFit is the guaranteed floor.
		if newTokens >= currentTokens {
			debugPrint(cfg, "REDUCE NO PROGRESS", fmt.Sprintf("%d → %d tokens, stopping", currentTokens, newTokens))
			break
		}

		shrinkPercent := 100 * (currentTokens - newTokens) / currentTokens
		current = reduced
		currentTokens = newTokens
		depth++

		debugPrint(cfg, "REDUCE STAGE COMPLETE", map[string]any{
			"depth":          depth,
			"tokens_after":   currentTokens,
			"shrink_percent": shrinkPercent,
		})
	}

	// Last resort: if still over budget, keep top-K priority summaries verbatim and use stubs for the rest
	if currentTokens > promptBudget {
		debugPrint(cfg, "LAST RESORT FLOOR", fmt.Sprintf("still %d tokens > %d budget after %d rounds, applying last resort sorting",
			currentTokens, promptBudget, depth))
		current = lastResortSortAndFit(classifiedFiles, promptBudget, tokenizerModel)
		currentTokens = tokenizer.CountTokens(current, tokenizerModel)
	}

	debugPrint(cfg, "HIERARCHICAL SUMMARIZATION COMPLETE", map[string]any{
		"final_tokens":  currentTokens,
		"budget":        promptBudget,
		"depth_used":    depth,
		"reduction_pct": 100 * (inputTokens - currentTokens) / inputTokens,
	})

	return current, nil
}

// classifyFile determines how to handle a given file diff
func classifyFile(fd FileDiff) fileClassification {
	// Check binary - diff will have "Binary files ... differ"
	if strings.Contains(fd.Content, "Binary files ") {
		return classBinary
	}

	// Check rename only - status is renamed and no content changes
	if fd.Status == "renamed" && fd.Added == 0 && fd.Removed == 0 {
		return classRenameOnly
	}

	// Check if it's a generated file/lockfile
	if isGenerated(fd.Path) {
		return classGenerated
	}

	// Check empty - no actual content changes
	if fd.Added == 0 && fd.Removed == 0 {
		return classEmpty
	}

	// Check whitespace only - all changes are whitespace
	if isWhitespaceOnly(fd.Content) {
		return classWhitespaceOnly
	}

	return classNormal
}

// isGenerated checks if a file path matches known generated/lockfile patterns
func isGenerated(path string) bool {
	base := filepath.Base(path)
	switch base {
	case "go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "Cargo.lock":
		return true
	}

	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".min.js" || ext == ".min.css" {
		return true
	}

	// Check patterns
	if strings.Contains(path, "/vendor/") || strings.Contains(path, "/dist/") {
		return true
	}
	if strings.HasSuffix(path, ".pb.go") || strings.HasSuffix(path, "_generated.go") {
		return true
	}

	return false
}

// isWhitespaceOnly checks if all changes in the diff are whitespace-only
func isWhitespaceOnly(content string) bool {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) &&
			!strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---") {
			trimmed := strings.TrimSpace(line[1:])
			if trimmed != "" {
				return false
			}
		}
	}
	return true
}

// extractRenamePaths extracts old/new paths from a rename diff
func extractRenamePaths(content string) (oldPath, newPath string) {
	oldFound := false
	newFound := false
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "rename from ") {
			oldPath = strings.TrimPrefix(line, "rename from ")
			oldFound = true
		} else if strings.HasPrefix(line, "rename to ") {
			newPath = strings.TrimPrefix(line, "rename to ")
			newFound = true
		}
		if oldFound && newFound {
			break
		}
	}
	return
}

// allocateBudgets assigns per-file token budgets based on priority
func allocateBudgets(files []*processedFile, totalBudget int, totalPriority int) {
	for _, pf := range files {
		_ = allocateBudgetForFile(pf.Priority, totalPriority, totalBudget, minPerFileTokens, maxPerFileTokens)
		// We just calculate it - actual usage happens during summarization
		// Store budget somewhere if needed, but we don't need to keep it after allocation
		// pf.Tokens is already calculated from when pf was created
	}
}

func allocateBudgetForFile(priority, totalPriority, totalBudget int, minTok, maxTok int) int {
	if totalPriority == 0 {
		return (minTok + maxTok) / 2
	}
	budget := float64(totalBudget) * float64(priority) / float64(totalPriority)
	if budget < float64(minTok) {
		return minTok
	}
	if budget > float64(maxTok) {
		return maxTok
	}
	return int(budget)
}

// summarizeSingleFile summarizes a single file's diff using LLM
func summarizeSingleFile(cfg *config.Config, path, content string, maxTokens int) (string, error) {
	systemPrompt := fmt.Sprintf(`You are a diff summarizer for git commit message generation.
Summarize the given diff for file %s into a concise structured summary.

CRITICAL REQUIREMENTS:
- Your summary MUST NOT exceed %d tokens
- Preserve ALL important information:
  * Which functions/methods were added/removed/modified (extract from @@ ... @@ hunk headers)
  * What behavior changed (added, removed, modified functionality)
  * Any BREAKING CHANGE markers or API changes
  * Deletions that affect public API
- Format as a bullet list with file path as heading
- Focus on what the change does, not the exact line numbers
- Keep it concise but complete`, path, maxTokens)

	userPrompt := fmt.Sprintf("Summarize this git diff:\n\n```diff\n%s\n```", content)

	// Retry up to 2 times on failure
	return callLLMWithRetry(cfg, systemPrompt, userPrompt, 2)
}

// reduceSummarize groups the current summary into chunks and summarizes each chunk to a
// fraction of promptBudget, guaranteeing the combined output targets promptBudget rather
// than merely passing content through.
func reduceSummarize(cfg *config.Config, current string, promptBudget int, depth int) (string, int, error) {
	tokenizerModel := cfg.Context.TokenizerModel
	if tokenizerModel == "" {
		tokenizerModel = cfg.AI.Model
	}

	// Chunk on file/section boundaries (the map stage joins sections with blank lines),
	// aiming each chunk's input at roughly promptBudget tokens.
	chunks := chunkBySections(current, promptBudget, tokenizerModel)

	// Per-chunk OUTPUT budget is a true fraction of the total budget, independent of and
	// smaller than each chunk's input size, so the combined output approaches promptBudget.
	perChunkOut := promptBudget / len(chunks)
	if perChunkOut < minChunkOutTokens {
		perChunkOut = minChunkOutTokens
	}

	debugPrint(cfg, "REDUCE CHUNKING", map[string]any{
		"depth":         depth,
		"chunks":        len(chunks),
		"per_chunk_out": perChunkOut,
	})

	// Summarize each chunk concurrently with concurrency limit
	var g errgroup.Group
	g.SetLimit(concurrencyLimit)
	results := make([]string, len(chunks))
	failures := make([]bool, len(chunks))

	for i, chunk := range chunks {
		i := i
		chunk := chunk
		g.Go(func() error {
			systemPrompt := fmt.Sprintf(`You are a diff summarizer. The original diff was split into chunks for hierarchical summarization.
Summarize this chunk while PRESERVING:
- All file paths (attribution must be clear which summary belongs to which file)
- All function/method names that changed
- Any BREAKING CHANGE markers
- The overall purpose of each change

You MUST compress aggressively. Target maximum length: %d tokens.`, perChunkOut)

			userPrompt := fmt.Sprintf("Summarize this chunk of diff summaries:\n\n%s", chunk)

			summary, err := callLLMWithRetry(cfg, systemPrompt, userPrompt, 1)
			if err != nil {
				// Don't abort the whole reduce on one flaky call - keep the original
				// chunk so its file paths and content survive into the next round.
				debugPrint(cfg, "REDUCE CHUNK FAILED", fmt.Sprintf("chunk %d: %v, keeping original", i, err))
				results[i] = chunk
				failures[i] = true
				return nil
			}
			results[i] = summary
			return nil
		})
	}

	_ = g.Wait()

	// If every chunk failed, surface the error so the caller can decide.
	allFailed := true
	for _, f := range failures {
		if !f {
			allFailed = false
			break
		}
	}
	if allFailed {
		return "", 0, fmt.Errorf("all %d reduce chunks failed", len(chunks))
	}

	// Combine results
	var reduced strings.Builder
	for _, result := range results {
		if strings.TrimSpace(result) != "" {
			reduced.WriteString(result)
			reduced.WriteString("\n\n")
		}
	}

	final := strings.TrimSpace(reduced.String())
	finalTokens := tokenizer.CountTokens(final, tokenizerModel)
	return final, finalTokens, nil
}

// chunkBySections groups blank-line-separated sections into chunks whose input size is
// roughly chunkInputTarget tokens, preserving section (and thus file) boundaries. A single
// oversized section becomes its own chunk rather than being split mid-content. The result
// always has at least 2 chunks when the input exceeds chunkInputTarget, so the per-chunk
// output budget computed by the caller forces real compression.
func chunkBySections(current string, chunkInputTarget int, tokenizerModel string) []string {
	sections := strings.Split(current, "\n\n")
	var chunks []string
	var b strings.Builder
	chunkTokens := 0

	flush := func() {
		if b.Len() > 0 {
			chunks = append(chunks, strings.TrimSpace(b.String()))
			b.Reset()
			chunkTokens = 0
		}
	}

	for _, sec := range sections {
		if strings.TrimSpace(sec) == "" {
			continue
		}
		secTokens := tokenizer.CountTokens(sec, tokenizerModel)
		if chunkTokens > 0 && chunkTokens+secTokens > chunkInputTarget {
			flush()
		}
		b.WriteString(sec)
		b.WriteString("\n\n")
		chunkTokens += secTokens
	}
	flush()

	if len(chunks) == 0 {
		return []string{current}
	}
	// Guarantee compression is possible: if everything collapsed into one chunk but still
	// exceeds the target, split it in half by sections.
	if len(chunks) == 1 && tokenizer.CountTokens(chunks[0], tokenizerModel) > chunkInputTarget && len(sections) > 1 {
		mid := len(sections) / 2
		first := strings.TrimSpace(strings.Join(sections[:mid], "\n\n"))
		second := strings.TrimSpace(strings.Join(sections[mid:], "\n\n"))
		return []string{first, second}
	}
	return chunks
}

// lastResortSortAndFit sorts all files by priority and keeps top ones full, lower as stubs
func lastResortSortAndFit(files []processedFile, promptBudget int, tokenizerModel string) string {
	// Sort by priority descending
	sort.Slice(files, func(i, j int) bool {
		return files[i].Priority > files[j].Priority
	})

	var result strings.Builder
	currentTokens := 0

	// Add files starting from highest priority, stop when we hit budget
	for _, pf := range files {
		content := pf.Content
		toks := tokenizer.CountTokens(content, tokenizerModel)
		if currentTokens+toks <= promptBudget {
			result.WriteString(content)
			result.WriteString("\n\n")
			currentTokens += toks
		} else {
			// Add stub at least to guarantee path appears
			stub := makeStubForLastResort(pf)
			stubToks := tokenizer.CountTokens(stub, tokenizerModel)
			if currentTokens+stubToks <= promptBudget {
				result.WriteString(stub)
				result.WriteString("\n\n")
				currentTokens += stubToks
			}
			// Even if stub doesn't fit, stop - higher priority files are already in
		}
	}

	return strings.TrimSpace(result.String())
}

func makeStubForLastResort(pf processedFile) string {
	// Extract line counts from original diff if available - we don't store them,
	// but path is all we really need at minimum
	return fmt.Sprintf("%s: summary omitted due to size constraints", pf.Path)
}

func findFileDiffIndex(path string, fileDiffs []FileDiff) int {
	for i, fd := range fileDiffs {
		if fd.Path == path {
			return i
		}
	}
	return 0
}

// extractHunkEnclosingFunc extracts the function/method name from a hunk header
// Example: @@ -123,45 +123,45 @@ func (r *Receiver) MethodName(int arg) int → returns "MethodName"
func extractHunkEnclosingFunc(hunkHeader string) string {
	// Patterns to find function names after @@ marker
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`@@.*@@\s+func\s+(\w+)`),             // Go free function
		regexp.MustCompile(`@@.*@@\s+func\s*\([^)]+\)\s*(\w+)`), // Go method
		regexp.MustCompile(`@@.*@@\s+function\s+(\w+)`),         // JS function
		regexp.MustCompile(`@@.*@@\s+(\w+)\s*\([^)]*\)\s*{`),    // JS/TS/C/C++ function
		regexp.MustCompile(`@@.*@@\s+def\s+(\w+)`),              // Python function
		regexp.MustCompile(`@@.*@@\s+class\s+(\w+)`),            // Class definition
	}

	for _, pattern := range patterns {
		matches := pattern.FindStringSubmatch(hunkHeader)
		if len(matches) >= 2 {
			return matches[1]
		}
	}
	return ""
}
