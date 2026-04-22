package ai

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/johnstilia/commitron/pkg/config"
	"github.com/johnstilia/commitron/pkg/tokenizer"
)

// FileDiff represents a single file's diff information
type FileDiff struct {
	Path    string // File path
	Status  string // "added", "modified", "deleted", "renamed"
	Added   int    // Lines added
	Removed int    // Lines removed
	Content string // Raw diff content for this file
	Summary string // Generated summary
}

// FileWithPriority represents a file with its priority score and token count
type FileWithPriority struct {
	FileDiff
	Priority int // Priority score (0-200+)
	Tokens   int // Token count for this file's diff
}

// ParseDiffByFile splits a git diff into per-file chunks
func ParseDiffByFile(diff string) []FileDiff {
	var files []FileDiff

	// Split by "diff --git" markers
	parts := regexp.MustCompile(`(?m)^diff --git`).Split(diff, -1)

	for i, part := range parts {
		if i == 0 && !strings.HasPrefix(diff, "diff --git") {
			// Skip any content before the first diff
			continue
		}

		if strings.TrimSpace(part) == "" {
			continue
		}

		// Re-add the "diff --git" prefix (except for the first split which didn't have it)
		if i > 0 || strings.HasPrefix(diff, "diff --git") {
			part = "diff --git" + part
		}

		file := parseSingleFileDiff(part)
		if file.Path != "" {
			files = append(files, file)
		}
	}

	return files
}

// parseSingleFileDiff extracts information from a single file's diff
func parseSingleFileDiff(diff string) FileDiff {
	file := FileDiff{
		Content: diff,
		Status:  "modified",
	}

	lines := strings.Split(diff, "\n")

	for _, line := range lines {
		// Extract file path from "diff --git a/path b/path"
		if strings.HasPrefix(line, "diff --git") {
			re := regexp.MustCompile(`diff --git a/(\S+) b/(\S+)`)
			matches := re.FindStringSubmatch(line)
			if len(matches) >= 3 {
				file.Path = matches[2] // Use the 'b/' path (destination)
			}
		}

		// Detect file status
		if strings.HasPrefix(line, "new file mode") {
			file.Status = "added"
		} else if strings.HasPrefix(line, "deleted file mode") {
			file.Status = "deleted"
		} else if strings.HasPrefix(line, "rename from") {
			file.Status = "renamed"
		}

		// Count added/removed lines
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			file.Added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			file.Removed++
		}
	}

	return file
}

// stubFileSummary creates a concise deterministic stub summary of a single file's changes
// Used as fallback when LLM summarization fails or for pre-classified files
func stubFileSummary(fd FileDiff) string {
	var summary strings.Builder

	// File header with status and line counts
	summary.WriteString(fmt.Sprintf("File: %s ", fd.Path))

	switch fd.Status {
	case "added":
		summary.WriteString("(new file, ")
	case "deleted":
		summary.WriteString("(deleted, ")
	case "renamed":
		summary.WriteString("(renamed, ")
	default:
		summary.WriteString("(")
	}

	summary.WriteString(fmt.Sprintf("+%d, -%d)\n", fd.Added, fd.Removed))

	// Extract function/class names and key changes
	funcNames := extractFunctionNames(fd.Content)
	if len(funcNames) > 0 {
		// Separate added and removed functions for clarity
		var addedFuncs []string
		var removedFuncs []string
		for _, fn := range funcNames {
			if strings.HasPrefix(fn, "removed:") {
				removedFuncs = append(removedFuncs, strings.TrimPrefix(fn, "removed:"))
			} else {
				addedFuncs = append(addedFuncs, fn)
			}
		}

		if len(addedFuncs) > 0 {
			summary.WriteString(fmt.Sprintf("  Added/Modified: %s\n", strings.Join(addedFuncs, ", ")))
		}
		if len(removedFuncs) > 0 {
			summary.WriteString(fmt.Sprintf("  Removed: %s\n", strings.Join(removedFuncs, ", ")))
		}
	}

	// Add a few key code snippets (max 5 lines of actual changes)
	keyChanges := extractKeyChanges(fd.Content, 5)
	if len(keyChanges) > 0 {
		summary.WriteString("  Key changes:\n")
		for _, change := range keyChanges {
			summary.WriteString(fmt.Sprintf("    %s\n", change))
		}
	}

	return summary.String()
}

// extractFunctionNames finds function/method names in the diff (both added and removed)
func extractFunctionNames(diff string) []string {
	var added []string
	var removed []string
	seenAdded := make(map[string]bool)
	seenRemoved := make(map[string]bool)

	// Patterns for different languages (capture group for function name)
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`^[+-].*func\s+(\w+)`),                          // Go functions
		regexp.MustCompile(`^[+-].*function\s+(\w+)`),                      // JavaScript functions
		regexp.MustCompile(`^[+-].*def\s+(\w+)`),                           // Python functions
		regexp.MustCompile(`^[+-].*class\s+(\w+)`),                         // Class definitions
		regexp.MustCompile(`^[+-].*(\w+)\s*\([^)]*\)\s*{`),                 // Generic function patterns
		regexp.MustCompile(`^[+-].*(?:public|private|protected)\s+\w+\s+(\w+)\(`), // Java/C++ methods
	}

	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		isAddition := strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++")
		isDeletion := strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---")

		if !isAddition && !isDeletion {
			continue
		}

		for _, pattern := range patterns {
			matches := pattern.FindStringSubmatch(line)
			if len(matches) >= 2 {
				name := matches[1]
				if len(name) > 0 {
					if isAddition && !seenAdded[name] {
						added = append(added, name+"()")
						seenAdded[name] = true
					} else if isDeletion && !seenRemoved[name] {
						removed = append(removed, name+"()")
						seenRemoved[name] = true
					}
				}
			}
		}
	}

	// Combine results, prioritizing additions but including deletions
	var result []string
	for _, fn := range added {
		result = append(result, fn)
		if len(result) >= 5 {
			return result
		}
	}
	for _, fn := range removed {
		// Mark removed functions
		result = append(result, "removed:"+fn)
		if len(result) >= 5 {
			return result
		}
	}

	return result
}

// extractKeyChanges extracts the most significant added/removed lines
func extractKeyChanges(diff string, maxLines int) []string {
	var additions []string
	var deletions []string

	lines := strings.Split(diff, "\n")
	for _, line := range lines {
		// Check for additions
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			trimmed := strings.TrimSpace(line[1:])
			// Skip empty lines, just braces, or imports
			if trimmed != "" && trimmed != "{" && trimmed != "}" && !strings.HasPrefix(trimmed, "import ") {
				additions = append(additions, "+"+trimmed)
			}
		}
		// Check for deletions
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			trimmed := strings.TrimSpace(line[1:])
			// Skip empty lines, just braces, or imports
			if trimmed != "" && trimmed != "{" && trimmed != "}" && !strings.HasPrefix(trimmed, "import ") {
				deletions = append(deletions, "-"+trimmed)
			}
		}
	}

	// Combine additions and deletions, prioritizing additions but including deletions
	var changes []string

	// Add up to half maxLines from additions
	addLimit := maxLines / 2
	if addLimit == 0 {
		addLimit = maxLines
	}
	for i := 0; i < len(additions) && len(changes) < addLimit; i++ {
		changes = append(changes, additions[i])
	}

	// Fill remaining slots with deletions
	for i := 0; i < len(deletions) && len(changes) < maxLines; i++ {
		changes = append(changes, deletions[i])
	}

	return changes
}

// PrioritizeFiles scores files by importance for commit message generation
func PrioritizeFiles(files []FileDiff) []FileWithPriority {
	var prioritized []FileWithPriority

	for _, file := range files {
		priority := calculateFilePriority(file)
		tokens := tokenizer.CountTokens(file.Content, "gpt-4") // Use gpt-4 as baseline

		prioritized = append(prioritized, FileWithPriority{
			FileDiff: file,
			Priority: priority,
			Tokens:   tokens,
		})
	}

	// Sort by priority (highest first)
	sort.Slice(prioritized, func(i, j int) bool {
		return prioritized[i].Priority > prioritized[j].Priority
	})

	return prioritized
}

// calculateFilePriority scores a file based on its importance
func calculateFilePriority(file FileDiff) int {
	score := 0
	path := file.Path

	// Core logic files get high priority
	if strings.Contains(path, "pkg/ai/") {
		score += 100
	} else if strings.Contains(path, "pkg/git/") {
		score += 80
	} else if strings.Contains(path, "cmd/") {
		score += 60
	} else if strings.Contains(path, "pkg/") {
		score += 40
	}

	// Change magnitude (capped at 50)
	totalChanges := file.Added + file.Removed
	score += min(totalChanges, 50)

	// File type bonuses/penalties
	if strings.HasSuffix(path, ".go") {
		score += 30
	} else if strings.HasSuffix(path, "_test.go") {
		score -= 20 // Tests are lower priority
	} else if strings.HasSuffix(path, ".md") {
		score -= 30 // Docs are lower priority
	} else if strings.HasSuffix(path, ".json") || strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml") {
		score += 10 // Config files are somewhat important
	}

	// New files are interesting
	if file.Status == "added" {
		score += 20
	}

	// Deleted files are less interesting for future context
	if file.Status == "deleted" {
		score -= 30
	}

	return max(score, 0)
}

// BuildContextFromDiff intelligently builds context within token limits using hierarchical summarization
func BuildContextFromDiff(diff string, maxTokens int, cfg *config.Config) (string, error) {
	return HierarchicalSummarize(cfg, diff, maxTokens)
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// max returns the maximum of two integers
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
