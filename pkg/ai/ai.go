package ai

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/johnstilia/commitron/pkg/config"
	"github.com/johnstilia/commitron/pkg/tokenizer"
	"github.com/johnstilia/commitron/pkg/ui"
)

// Template constants for different commit message formats
const (
	// Base template with common fields
	BaseTemplateJSON = `{
		"instruction": "Generate a commit message describing the changes",
		"format": {
			"max_subject_length": %d,
			"max_body_length": %d,
			"include_body": %t
		},
		"context": {
			"files": %s,
			"changes": %s
		},
		"output": {
			"format": "json",
			"subject_only": %t
		}
	}`

	// Template for conventional commits
	ConventionalCommitsJSON = `{
		"instruction": "Generate a commit message following Conventional Commits specification",
		"requirements": {
			"must_start_with_type": true,
			"must_have_subject": true,
			"format_examples": [
				"feat: add new user authentication",
				"fix(auth): resolve login timeout issue",
				"docs: update README installation steps"
			],
			"invalid_formats": [
				": description without type",
				"feature: (incorrect type name)"
			]
		},
		"convention": {
			"type": "conventional",
			"types": {
				"docs": "Documentation only changes",
				"style": "Changes that do not affect the meaning of the code (whitespace, formatting, etc)",
				"refactor": "A code change that neither fixes a bug nor adds a feature",
				"perf": "A code change that improves performance",
				"test": "Adding missing tests or correcting existing tests",
				"build": "Changes that affect the build system or external dependencies",
				"ci": "Changes to CI configuration files and scripts",
				"chore": "Other changes that don't modify source or test files",
				"revert": "Reverts a previous commit",
				"feat": "A new feature",
				"fix": "A bug fix"
			},
			"format": "type(scope): subject",
			"rules": {
				"commit_structure": "<type>[optional scope]: <description>\\n\\n[optional body]\\n\\n[optional footer(s)]",
				"breaking_change": "A commit with footer 'BREAKING CHANGE:' or with '!' after type/scope introduces a breaking API change",
				"scope_format": "A scope MAY be provided in parentheses after the type",
				"type_case": "Types MUST be lowercase",
				"description_format": "Description MUST immediately follow the colon and space",
				"body_format": "A longer commit body MUST be provided after a blank line following the description when include_body is true",
				"footer_format": "Footer MUST be separated by a blank line and follow the format 'token: value'",
				"breaking_format": "Breaking changes MUST be indicated with '!' before colon or as 'BREAKING CHANGE:' in footer"
			}
		},
		"format": {
			"max_subject_length": %d,
			"max_body_length": %d,
			"include_body": %t,
			"body_required": %t,
			"critical_note": "CRITICAL: The TOTAL combined length of 'type(scope): subject' MUST NOT exceed max_subject_length. This includes ALL characters. Keep subject extremely brief.",
			"length_examples": "Examples of good length subjects: 'fix: update validation logic', 'feat(auth): add login timeout'"
		},
		"context": {
			"files": %s,
			"changes": %s
		},
		"output": {
			"format": "json",
			"subject_only": %t,
			"response_format": {
				"type": "",
				"scope": "",
				"subject": "",
				"body": ""
			}
		}
	}`

	// Template for custom commit format
	CustomCommitJSON = `{
		"instruction": "Generate a commit message following custom template",
		"convention": {
			"type": "custom",
			"template": "%s"
		},
		"format": {
			"max_subject_length": %d,
			"max_body_length": %d,
			"include_body": %t
		},
		"context": {
			"files": %s,
			"changes": %s
		},
		"output": {
			"format": "json",
			"subject_only": %t
		}
	}`
)

// CommitTypeFormats defines the format for different commit types
var CommitTypeFormats = map[string]string{
	"":             "<commit message>",
	"conventional": "<type>(<optional scope>): <commit message>",
}

// CommitTypeDescriptions maps commit types to their descriptions for AI guidance
var CommitTypeDescriptions = map[string]string{
	"": "",
	"conventional": `Choose a type from the type-to-description JSON below that best describes the code changes:
{
  "docs": "Documentation only changes",
  "style": "Changes that do not affect the meaning of the code (whitespace, formatting, missing semi-colons, etc)",
  "refactor": "A code change that neither fixes a bug nor adds a feature",
  "perf": "A code change that improves performance",
  "test": "Adding missing tests or correcting existing tests",
  "build": "Changes that affect the build system or external dependencies",
  "ci": "Changes to CI configuration files and scripts",
  "chore": "Other changes that don't modify source or test files",
  "revert": "Reverts a previous commit",
  "feat": "A new feature",
  "fix": "A bug fix"
}`,
}

// ConventionalCommitRules contains the specification for conventional commits
const ConventionalCommitRules = `
Conventional Commits 1.0.0 Rules:

1. Commit messages MUST be structured as follows:
   <type>[optional scope]: <description>
   [optional body]
   [optional footer(s)]

2. Types:
   - fix: patches a bug (correlates with PATCH in SemVer)
   - feat: introduces a new feature (correlates with MINOR in SemVer)
   - Other types allowed: build, chore, ci, docs, style, refactor, perf, test

3. BREAKING CHANGE:
   - A commit with footer "BREAKING CHANGE:" or with "!" after type/scope introduces a breaking API change
   - Example: feat!: breaking change or feat: new feature with footer BREAKING CHANGE: description

4. Scope:
   - A scope MAY be provided in parentheses after the type: feat(parser): add ability to parse arrays

5. Format Rules:
   - Types MUST be lowercase (feat, fix, docs, etc.)
   - Description MUST immediately follow the colon and space
   - A longer commit body MUST be provided after a blank line following the description when include_body is true
   - A body is required when include_body is set to true, otherwise it is optional
   - When provided, the body must be meaningful and explain what changes were made and why
   - Footer MUST be separated by a blank line and follow the format "token: value" or "token # value"
   - Breaking changes MUST be indicated with "!" before colon or as "BREAKING CHANGE:" in footer

6. Examples:
   - fix: correct minor typos in code
   - feat(api): add ability to search by date
   - docs: correct spelling of CHANGELOG
   - feat!: send email when product is shipped (breaking change)
   - feat: add user authentication

     Implement secure user authentication with password hashing and session management.
`

// CommitMessage represents a structured commit message
type CommitMessage struct {
	Type    string `json:"type"`
	Scope   string `json:"scope"`
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// EnhancedFileInfo contains detailed information about a changed file
type EnhancedFileInfo struct {
	Path             string `json:"path"`              // File path
	AddedLines       int    `json:"added_lines"`       // Number of added lines
	RemovedLines     int    `json:"removed_lines"`     // Number of removed lines
	Summary          string `json:"summary"`           // Brief description of the file
	FirstLines       string `json:"first_lines"`       // First N lines of the file
	FileType         string `json:"file_type"`         // Type of the file based on extension
	PercentageChange string `json:"percentage_change"` // Percentage of the file that was changed
}

// FormatCommitMessage formats a CommitMessage into a string according to the configuration
func FormatCommitMessage(msg CommitMessage, cfg *config.Config) string {
	var result strings.Builder

	// Format the subject line according to convention
	switch cfg.Commit.Convention {
	case config.ConventionalCommits:
		if msg.Scope != "" {
			result.WriteString(fmt.Sprintf("%s(%s): %s", msg.Type, msg.Scope, msg.Subject))
		} else {
			result.WriteString(fmt.Sprintf("%s: %s", msg.Type, msg.Subject))
		}
	case config.CustomConvention:
		// For custom convention, we assume the AI has already formatted according to template
		result.WriteString(msg.Subject)
	default:
		result.WriteString(msg.Subject)
	}

	// Add body if configured and provided - format as bullet points
	if cfg.Commit.IncludeBody && msg.Body != "" {
		result.WriteString("\n\n")

		// Format body as bullet points if it's not already formatted
		bodyLines := strings.Split(strings.TrimSpace(msg.Body), "\n")
		for _, line := range bodyLines {
			line = strings.TrimSpace(line)
			if line != "" {
				// Add bullet point if not already present
				if !strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "* ") {
					result.WriteString("- ")
				}
				result.WriteString(line)
				result.WriteString("\n")
			}
		}
		// Remove trailing newline
		resultStr := result.String()
		result.Reset()
		result.WriteString(strings.TrimSuffix(resultStr, "\n"))
	}

	return result.String()
}

// GenerateTextPrompt creates the user message for commit message generation.
//
// Prompt size is latency: prompt evaluation dominates wall time on self-hosted
// endpoints, so the instructions are stated once, precisely, instead of being
// repeated for emphasis. Repetition also costs quality - restating the same rule in
// six competing phrasings gives a model contradictory targets to satisfy.
func GenerateTextPrompt(cfg *config.Config, files []string, changes string) string {
	var p []string

	if cfg.Commit.Convention == config.ConventionalCommits {
		p = append(p, fmt.Sprintf(
			"Write a git commit message for the staged changes below.\n\n"+
				"Format:\n"+
				"<type>(<optional scope>): <subject>\n"+
				"<blank line>\n"+
				"- bullet\n"+
				"- bullet\n\n"+
				"Rules:\n"+
				"- type is exactly one of: feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert\n"+
				"- scope is optional, lowercase, one word naming the area touched (e.g. ai, git, config)\n"+
				"- subject: imperative mood, lowercase, no trailing period; the whole first line "+
				"(type, scope, colon and subject together) must be under %d characters\n"+
				"- pick the type from what the change actually does; if it does several things, "+
				"pick the one a reader would care about most",
			cfg.Commit.MaxLength))
	} else {
		p = append(p, fmt.Sprintf(
			"Write a git commit message for the staged changes below.\n\n"+
				"Rules:\n"+
				"- first line: imperative mood, under %d characters, no trailing period",
			cfg.Commit.MaxLength))
	}

	if cfg.Commit.IncludeBody {
		p = append(p, fmt.Sprintf(
			"- body: a blank line, then one \"- \" bullet per meaningful change, most important first\n"+
				"- each bullet names what concretely changed (the function, type, flag, endpoint or "+
				"behaviour) and what it does now - a reader who cannot see the diff should understand "+
				"the change from the bullet alone\n"+
				"- cover every significant change, grouping trivial related edits into one bullet\n"+
				"- explain *why* where the diff makes it clear (a fix, a bound, a removed workaround)\n"+
				"- no line counts, no bare file listings, no \"+X/-Y\", no restating the subject\n"+
				"- keep the body under %d characters",
			cfg.Commit.MaxBodyLength))
	} else {
		p = append(p, "- output the subject line only, no body")
	}

	p = append(p, "Output only the commit message itself. No preamble, no commentary, no code fences.")

	// Recent history teaches the repo's own conventions (scope vocabulary, phrasing,
	// level of detail) far more cheaply and reliably than abstract style rules.
	if history := GetRecentCommitSubjects(cfg); history != "" {
		p = append(p, "Recent commits in this repository, for style reference only - do not describe them:\n"+history)
	}

	if cfg.Context.IncludeFileNames && len(files) > 0 {
		p = append(p, fmt.Sprintf("Files changed (%d):\n%s", len(files), strings.Join(files, "\n")))
	}

	if cfg.Context.IncludeDiff && strings.TrimSpace(changes) != "" {
		p = append(p, "Changes:\n\n"+changes)
	}

	return strings.Join(p, "\n\n")
}

// GetRecentCommitSubjects returns recent commit subject lines so the model can match
// the repository's established commit style. Returns an empty string on any failure -
// style context is a bonus, never a reason to fail commit generation.
func GetRecentCommitSubjects(cfg *config.Config) string {
	n := cfg.Context.IncludeRecentCommits
	if n <= 0 {
		return ""
	}

	out, err := exec.Command("git", "log", "--no-merges", "--format=%s", fmt.Sprintf("-n%d", n)).Output()
	if err != nil {
		return ""
	}

	var subjects []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		// Release automation commits are noise as style examples.
		if line == "" || strings.Contains(line, "[skip ci]") {
			continue
		}
		subjects = append(subjects, "  "+line)
	}

	return strings.Join(subjects, "\n")
}

// reasoningTags are the opening tags reasoning-capable models emit when their chain of
// thought leaks into the content stream instead of a separate field.
var reasoningTags = []string{"<think>", "<thinking>", "<reasoning>"}

// stripReasoningBlocks removes leaked chain-of-thought from a model response.
//
// A tag only counts as leaked reasoning when it opens a line, which is how models emit
// it. A tag appearing mid-line is prose the model wrote about reasoning tags - a commit
// message that describes this very code will contain one - and must survive verbatim.
// Treating those as block openers truncated the body at the mention.
func stripReasoningBlocks(response string) string {
	for _, open := range reasoningTags {
		closeTag := strings.Replace(open, "<", "</", 1)
		for offset := 0; ; {
			idx := strings.Index(response[offset:], open)
			if idx < 0 {
				break
			}
			start := offset + idx
			if start > 0 && response[start-1] != '\n' {
				// Mid-line mention, not a block opener: skip past it and keep looking.
				offset = start + len(open)
				continue
			}
			end := strings.Index(response[start:], closeTag)
			if end < 0 {
				// Unterminated block: everything after it is reasoning, not a message.
				response = response[:start]
				break
			}
			response = response[:start] + response[start+end+len(closeTag):]
			offset = start
		}
	}
	return response
}

// sanitizeResponse strips wrappers that reasoning-capable and chat-tuned models put
// around a commit message. Without this the first line of the response - a stray code
// fence or a leftover <think> block - would be parsed as the subject line.
func sanitizeResponse(response string) string {
	response = stripReasoningBlocks(response)

	lines := strings.Split(strings.TrimSpace(response), "\n")

	// Drop a leading fence (```/```text) and its matching closer.
	for len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
		lines = lines[1:]
		for i := len(lines) - 1; i >= 0; i-- {
			if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
				lines = append(lines[:i], lines[i+1:]...)
				break
			}
		}
	}

	// Drop leading blank lines and conversational preamble before the subject.
	for len(lines) > 0 {
		first := strings.TrimSpace(lines[0])
		if first == "" {
			lines = lines[1:]
			continue
		}
		lower := strings.ToLower(first)
		if strings.HasSuffix(first, ":") && !strings.Contains(first, " ") {
			break // looks like "feat:" style, keep it
		}
		if strings.HasPrefix(lower, "here is") || strings.HasPrefix(lower, "here's") ||
			strings.HasPrefix(lower, "sure,") || strings.HasPrefix(lower, "commit message:") {
			lines = lines[1:]
			continue
		}
		break
	}

	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// ParseCommitMessageJSON attempts to parse a JSON response into a CommitMessage struct
func ParseCommitMessageJSON(response string) (CommitMessage, error) {
	var msg CommitMessage
	var parseErr error

	response = sanitizeResponse(response)

	// A JSON object only counts as a commit message once it carries a subject. When one
	// parses but has no subject the response is a failed generation, not free text: the
	// text parser would otherwise read the raw braces as "type: subject" and produce a
	// commit message made of JSON punctuation.
	// First try to extract JSON from the response if it contains other text
	jsonStr := extractJSON(response)
	if jsonStr != "" {
		// Try to unmarshal the extracted JSON
		var candidate CommitMessage
		if err := json.Unmarshal([]byte(jsonStr), &candidate); err == nil {
			if strings.TrimSpace(candidate.Subject) == "" {
				return candidate, fmt.Errorf("model returned a JSON object with no subject")
			}
			return candidate, nil
		} else {
			parseErr = err
		}
	}

	// Next, try to unmarshal the whole response as JSON
	if err := json.Unmarshal([]byte(response), &msg); err == nil {
		if strings.TrimSpace(msg.Subject) == "" {
			return msg, fmt.Errorf("model returned a JSON response with no subject")
		}
		// Successfully parsed whole response as JSON
		return msg, nil
	} else if parseErr == nil {
		parseErr = err
	}

	// If both JSON parsing attempts failed, try to parse as text
	extractedMsg := parseTextCommitMessage(response)

	// A response with no subject carries no commit message, whatever type was defaulted
	// onto it. Returning one anyway is how an empty completion became a bare "chore:"
	// commit: parseTextCommitMessage always fills in a type, so a type-only check here
	// never fired and the caller saw success.
	if strings.TrimSpace(extractedMsg.Subject) == "" {
		return extractedMsg, fmt.Errorf("no commit subject found in model response: %v", parseErr)
	}

	// Return the text-parsed message with no error
	return extractedMsg, nil
}

// extractJSON attempts to extract a JSON object from text that might contain other content
func extractJSON(text string) string {
	// Look for JSON object start and end
	start := strings.Index(text, "{")
	if start == -1 {
		return ""
	}

	// Find matching closing brace
	depth := 1
	for end := start + 1; end < len(text); end++ {
		switch text[end] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : end+1]
			}
		}
	}

	return ""
}

// parseTextCommitMessage attempts to parse a plain text commit message
func parseTextCommitMessage(text string) CommitMessage {
	lines := strings.Split(text, "\n")
	msg := CommitMessage{}

	// Look for [SUBJECT] and [BODY] markers
	subjectIndex := -1
	bodyIndex := -1

	for i, line := range lines {
		if strings.Contains(line, "[SUBJECT]") {
			subjectIndex = i
		} else if strings.Contains(line, "[BODY]") {
			bodyIndex = i
		}
	}

	// Handle [SUBJECT] tag if found
	if subjectIndex >= 0 && subjectIndex < len(lines)-1 {
		subject := lines[subjectIndex+1]

		// Clean up any remaining tags
		subject = strings.TrimSpace(strings.ReplaceAll(subject, "[SUBJECT]", ""))

		// Check for conventional commit format
		if idx := strings.Index(subject, ":"); idx > 0 {
			typeScope := subject[:idx]
			msg.Subject = strings.TrimSpace(subject[idx+1:])

			// Check for scope in parentheses
			if scopeStart := strings.Index(typeScope, "("); scopeStart > 0 {
				scopeEnd := strings.Index(typeScope, ")")
				if scopeEnd > scopeStart {
					msg.Type = typeScope[:scopeStart]
					msg.Scope = typeScope[scopeStart+1 : scopeEnd]
				} else {
					msg.Type = typeScope
				}
			} else {
				msg.Type = typeScope
			}
		} else {
			msg.Subject = subject
		}
	} else if len(lines) > 0 {
		// No [SUBJECT] tag found, use first line
		subject := strings.TrimSpace(lines[0])

		// Skip any leading ":" without a type (this fixes the issue of incorrect parsing)
		if strings.HasPrefix(subject, ": ") {
			subject = strings.TrimSpace(subject[2:])
			// Apply default type since no type was provided
			msg.Type = "chore"
			msg.Subject = subject
		} else if idx := strings.Index(subject, ":"); idx > 0 {
			// Check for conventional commit format with type
			typeScope := subject[:idx]
			msg.Subject = strings.TrimSpace(subject[idx+1:])

			// Check for scope in parentheses
			if scopeStart := strings.Index(typeScope, "("); scopeStart > 0 {
				scopeEnd := strings.Index(typeScope, ")")
				if scopeEnd > scopeStart {
					msg.Type = typeScope[:scopeStart]
					msg.Scope = typeScope[scopeStart+1 : scopeEnd]
				} else {
					msg.Type = typeScope
				}
			} else {
				msg.Type = typeScope
			}
		} else {
			// No conventional format found, default to chore type
			msg.Type = "chore"
			msg.Subject = subject
		}
	}

	// Ensure we have a valid type for conventional commits
	if msg.Type == "" {
		msg.Type = "chore" // Apply default type if none found
	}

	// Handle [BODY] tag if found
	if bodyIndex >= 0 && bodyIndex < len(lines)-1 {
		bodyLines := lines[bodyIndex+1:]
		// Remove any empty lines at the start of the body
		for len(bodyLines) > 0 && strings.TrimSpace(bodyLines[0]) == "" {
			bodyLines = bodyLines[1:]
		}
		if len(bodyLines) > 0 {
			msg.Body = strings.Join(bodyLines, "\n")
		}
	} else if len(lines) > 1 {
		// No [BODY] tag found, look for body after a blank line or double newline
		var bodyLines []string
		foundEmptyLine := false

		for i := 1; i < len(lines); i++ {
			line := lines[i]

			// First check if we've found an empty line to separate subject from body
			if !foundEmptyLine && strings.TrimSpace(line) == "" {
				foundEmptyLine = true
				continue
			}

			// If we've found an empty line separator, add subsequent non-empty lines to body
			if foundEmptyLine && strings.TrimSpace(line) != "" {
				bodyLines = append(bodyLines, line)
			}
		}

		// If no empty line was found but there are more lines after first line,
		// assume lines after first are the body (especially for text prompt format)
		if !foundEmptyLine && len(lines) > 2 {
			// Skip the first line (subject) and any immediate empty line
			startIdx := 1
			if strings.TrimSpace(lines[1]) == "" {
				startIdx = 2
			}
			bodyLines = lines[startIdx:]
		}

		if len(bodyLines) > 0 {
			msg.Body = strings.Join(bodyLines, "\n")
		}
	}

	// Clean up body (remove markdown formatting or template placeholders)
	if msg.Body != "" {
		// Remove placeholder text if it appears to be template text
		if strings.Contains(strings.ToLower(msg.Body), "<descriptive body") ||
			strings.Contains(strings.ToLower(msg.Body), "<commit message>") ||
			strings.Contains(strings.ToLower(msg.Body), "<optional body>") {
			msg.Body = ""
		}

		// Remove markdown code block delimiters if present
		msg.Body = strings.ReplaceAll(msg.Body, "```", "")

		// Remove common template markers
		msg.Body = strings.ReplaceAll(msg.Body, "[BODY]", "")

		// Some AIs return the word "Body:" at the start - remove it
		msg.Body = strings.TrimPrefix(strings.TrimSpace(msg.Body), "Body:")
		msg.Body = strings.TrimPrefix(strings.TrimSpace(msg.Body), "body:")

		// Ensure body is properly separated from subject
		if !strings.Contains(msg.Body, "\n\n") {
			msg.Body = "\n\n" + msg.Body
		}
	}

	// Ensure body is properly trimmed
	msg.Body = strings.TrimSpace(msg.Body)

	return msg
}

// DisplayStagedFiles prints the staged files in a modern TUI format
func DisplayStagedFiles(files []string) {
	// Get current branch name
	branch := "master" // Default if we can't get the branch
	cmdBranch := exec.Command("git", "branch", "--show-current")
	branchOutput, err := cmdBranch.Output()
	if err == nil {
		branch = strings.TrimSpace(string(branchOutput))
	}

	// Get staged and modified files counts
	stagedCount := len(files)
	modifiedCount := 0
	cmdStatus := exec.Command("git", "status", "--porcelain")
	statusOutput, err := cmdStatus.Output()
	if err == nil {
		for _, line := range strings.Split(string(statusOutput), "\n") {
			if len(line) > 0 && !strings.HasPrefix(line, "??") && !strings.HasPrefix(line, " ") {
				// Count modified but not staged files
				if !strings.HasPrefix(line, "A") && !strings.HasPrefix(line, "M") {
					modifiedCount++
				}
			}
		}
	}

	// Print header with branch and status
	fmt.Printf("\n\033[1;36mcommitron\033[0m \033[38;5;244m%s\033[0m", branch)
	if stagedCount > 0 {
		fmt.Printf(" \033[1;32m●%d\033[0m", stagedCount)
	}
	if modifiedCount > 0 {
		fmt.Printf(" \033[1;33m✚%d\033[0m", modifiedCount)
	}
	fmt.Println()

	// Print staged changes section
	fmt.Println("\n\033[1;36m📦 Staged Changes\033[0m")

	// Print files with icons based on file type
	for _, file := range files {
		// Get file extension and name
		ext := strings.ToLower(filepath.Ext(file))
		if ext != "" {
			ext = ext[1:] // Remove the dot
		}
		name := filepath.Base(file)

		// Get appropriate icon
		icon := ui.GetIconForFile(name, ext)
		fmt.Printf("   \033[38;5;244m%s\033[0m %s\n", icon, file)
	}

	// Print analyzing message
	fmt.Println("\n\033[1;36m🔍 Analyzing changes...\033[0m")
}

// wrapText wraps text at the specified width while preserving indentation
func wrapText(text string, width int, indent string) string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return ""
	}

	var lines []string
	currentLine := indent + words[0]

	for _, word := range words[1:] {
		if len(currentLine)+1+len(word) <= width {
			currentLine += " " + word
		} else {
			lines = append(lines, currentLine)
			currentLine = indent + word
		}
	}
	lines = append(lines, currentLine)

	return strings.Join(lines, "\n")
}

// DisplayCommitMessage shows the generated commit message with a modern UI
func DisplayCommitMessage(commitMsg string) (bool, error) {
	// Print header
	fmt.Println("\n\033[1;36m💬 Generated Commit Message\033[0m")
	fmt.Println("\033[38;5;244m────────────────────────\033[0m")

	// Display the commit message with proper formatting
	lines := strings.Split(commitMsg, "\n")
	inBody := false
	indentation := "   " // Base indentation for all lines

	for i, line := range lines {
		if line == "" {
			fmt.Println()
			if i < len(lines)-1 {
				inBody = true
			}
			continue
		}

		if inBody {
			// For body text, wrap at 80 characters
			// Check if line contains a file reference
			if strings.Contains(strings.ToLower(line), "file:") || strings.Contains(strings.ToLower(line), "files:") {
				// Extract file name if present
				parts := strings.Split(line, ":")
				if len(parts) > 1 {
					filePart := strings.TrimSpace(parts[1])
					// Try to extract file name from the text
					if strings.Contains(filePart, " ") {
						filePart = strings.Split(filePart, " ")[0]
					}
					// Get file extension and name
					ext := strings.ToLower(filepath.Ext(filePart))
					if ext != "" {
						ext = ext[1:] // Remove the dot
					}
					name := filepath.Base(filePart)
					// Get appropriate icon
					icon := ui.GetIconForFile(name, ext)
					// Replace the file name with icon + file name
					line = strings.Replace(line, filePart, icon+" "+filePart, 1)
				}
			}
			wrappedText := wrapText(line, 80, indentation)
			fmt.Printf("\033[38;5;252m%s\033[0m\n", wrappedText)
		} else {
			// For subject line, don't wrap
			fmt.Printf("%s\033[38;5;252m%s\033[0m\n", indentation, line)
		}
	}

	// Print confirmation prompt
	fmt.Println("\n\033[1;36m❓ Use this commit message?\033[0m")
	fmt.Print("\033[38;5;244m   [Y] Yes  [N] No\033[0m\n\n")

	// Get user input for confirmation
	fmt.Print("\033[1;36m> \033[0m")
	var response string
	_, err := fmt.Scanln(&response)
	if err != nil && err.Error() != "unexpected newline" {
		return false, err
	}

	// Convert response to lowercase for easier matching
	response = strings.ToLower(response)

	// Check if the response is affirmative
	return response == "y" || response == "yes" || response == "", nil
}

// DisplayAnalysisComplete prints a completion message
func DisplayAnalysisComplete() {
	fmt.Println("\033[1;32m✓ Analysis complete\033[0m")
}

// diffContextArg controls how many unchanged context lines surround each hunk in
// the staged diff sent to the model. "-U0" keeps every added/removed line plus the
// hunk headers (which carry the enclosing function names) while dropping unchanged
// surrounding lines, substantially cutting prompt size — and thus model prefill
// latency — with no loss of changed content.
const diffContextArg = "-U0"

// GetGitDiff returns clean git diff output for the staged files
func GetGitDiff(files []string) (string, error) {
	// Get clean git diff output without extra headers
	cmd := exec.Command("git", "diff", "--staged", diffContextArg)
	diffOutput, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("error getting git diff: %w", err)
	}

	return string(diffOutput), nil
}

// Token budgets for the final commit-message call.
const (
	// promptOverheadTokens is the size of everything in the prompt that is not the
	// diff: instructions, recent-commit style context and the file list.
	promptOverheadTokens = 1000

	// minDiffBudgetTokens keeps a usable amount of diff even when a small context
	// window and a large response budget would otherwise squeeze it to nothing.
	minDiffBudgetTokens = 500

	// maxResponseTokens caps generation for the final call. A commit message with a
	// full bulleted body runs a few hundred tokens; local endpoints generate at
	// roughly 10-50 tok/s, so an oversized cap only lets the model run long.
	maxResponseTokens = 1500

	// bodyCharsPerToken converts the configured body character limit into a rough
	// token budget, so a user asking for a long body still gets room to produce one.
	bodyCharsPerToken = 4
)

// responseTokenBudget returns the generation cap for the final commit-message call:
// enough for the configured body plus a reasoning model's thinking, but never the
// user's full context-sized budget.
func responseTokenBudget(cfg *config.Config) int {
	needed := 200 // subject, formatting and a short body
	if cfg.Commit.IncludeBody && cfg.Commit.MaxBodyLength > 0 {
		needed += cfg.Commit.MaxBodyLength / bodyCharsPerToken
	}
	if needed > maxResponseTokens {
		needed = maxResponseTokens
	}
	// Reasoning tokens are billed against the same ceiling as the visible answer, so a
	// cap sized for the message alone lets a reasoning model think until it is cut off
	// having emitted no message at all.
	if cfg.AI.ReasoningMaxTokens > 0 {
		needed += cfg.AI.ReasoningMaxTokens
	}
	// Respect an explicitly smaller user setting, but never inherit a huge one.
	if cfg.AI.MaxTokens > 0 && cfg.AI.MaxTokens < needed {
		return cfg.AI.MaxTokens
	}
	return needed
}

// generationBudgetHint names the setting that actually capped generation. An explicit
// ai.max_tokens below the computed budget is the binding limit, so pointing at
// ai.reasoning_max_tokens there would send the user to a knob that changes nothing.
func generationBudgetHint(cfg *config.Config, budget int) string {
	if cfg.AI.MaxTokens > 0 && cfg.AI.MaxTokens <= budget {
		return fmt.Sprintf("Raise ai.max_tokens (currently %d)", cfg.AI.MaxTokens)
	}
	return fmt.Sprintf("Raise ai.reasoning_max_tokens (currently %d)", cfg.AI.ReasoningMaxTokens)
}

// GenerateCommitMessage generates a commit message using the configured AI provider
func GenerateCommitMessage(cfg *config.Config, files []string, changes string) (string, error) {
	// Display staged files in TUI format if enabled
	if cfg.UI.EnableTUI {
		DisplayStagedFiles(files)
	}

	var err error

	// Token-aware processing
	tokenizerModel := cfg.Context.TokenizerModel
	if tokenizerModel == "" {
		tokenizerModel = cfg.AI.Model // Default to AI model
	}

	inputTokens := tokenizer.CountTokens(changes, tokenizerModel)
	maxTokens := cfg.Context.MaxInputTokens
	if maxTokens == 0 {
		maxTokens = tokenizer.GetProviderTokenLimit(string(cfg.AI.Provider), cfg.AI.Model)
	}

	// The diff budget is what actually governs latency: prompt evaluation is the
	// dominant cost of a commit-message call, and it scales with the diff we send.
	// It is deliberately far below the model's context limit - a commit message needs
	// to see what changed, not every line of it.
	diffBudget := cfg.Context.MaxDiffTokens
	if diffBudget <= 0 {
		diffBudget = defaultMaxDiffTokens
	}
	// Never exceed what the context window can actually hold alongside the response.
	responseTokens := responseTokenBudget(cfg)
	if ceiling := maxTokens - promptOverheadTokens - responseTokens; diffBudget > ceiling {
		diffBudget = ceiling
	}
	if diffBudget < minDiffBudgetTokens {
		diffBudget = minDiffBudgetTokens
	}

	// Reduce the diff deterministically, with no LLM round-trips. Each extra call to a
	// self-hosted endpoint costs another full prompt evaluation plus queue wait, so
	// summarizing a diff by asking the model about it is slower than simply sending it.
	if cfg.Context.IncludeDiff {
		changes = CompactDiff(changes, diffBudget, tokenizerModel)
	}
	compactedTokens := tokenizer.CountTokens(changes, tokenizerModel)

	if cfg.AI.Debug {
		debugPrint(cfg, "TOKEN ANALYSIS", map[string]any{
			"input_tokens":     inputTokens,
			"compacted_tokens": compactedTokens,
			"diff_budget":      diffBudget,
			"max_tokens":       maxTokens,
			"response_tokens":  responseTokens,
			"model":            tokenizerModel,
		})
	}

	if inputTokens > compactedTokens {
		fmt.Printf("\033[38;5;244m   %d → %d diff tokens\033[0m\n", inputTokens, compactedTokens)
	}

	// Debug: Show input data
	if cfg.AI.Debug {
		debugPrint(cfg, "INPUT FILES", files)
		debugPrint(cfg, "INPUT CHANGES (final)", fmt.Sprintf("%d chars, %d tokens", len(changes), compactedTokens))
		debugPrint(cfg, "CONFIG SETTINGS", map[string]any{
			"Convention":     cfg.Commit.Convention,
			"IncludeBody":    cfg.Commit.IncludeBody,
			"MaxLength":      cfg.Commit.MaxLength,
			"MaxBodyLength":  cfg.Commit.MaxBodyLength,
			"Provider":       cfg.AI.Provider,
			"Model":          cfg.AI.Model,
			"MaxInputTokens": cfg.Context.MaxInputTokens,
			"MaxDiffTokens":  diffBudget,
		})
	}

	var prompt string

	// Choose between JSON template approach and text prompt approach
	if cfg.Commit.Convention == config.ConventionalCommits {
		// Use the more detailed text prompt for conventional commits
		prompt = GenerateTextPrompt(cfg, files, changes)
	} else {
		// Use the JSON template approach for other conventions
		prompt = buildPrompt(cfg, files, changes)
	}

	// Debug: Show the prompt being sent to the AI
	debugPrint(cfg, "AI PROMPT", prompt)

	if cfg.AI.Debug {
		debugPrint(cfg, "FINAL TOKEN CHECK", map[string]any{
			"prompt_tokens":   tokenizer.CountTokens(prompt, tokenizerModel),
			"response_tokens": responseTokens,
			"max_tokens":      maxTokens,
		})
	}

	var rawResponse string

	// Get system prompt
	systemPrompt := getSystemPrompt(cfg)

	// Generation is billed in wall time too: a commit message needs a few hundred
	// tokens, so an unbounded response budget only buys the model room to ramble.
	rawResponse, err = CallLLM(withMaxTokens(cfg, responseTokens), systemPrompt, prompt)

	if err != nil {
		debugPrint(cfg, "AI ERROR", err.Error())
		if errors.Is(err, ErrEmptyCompletion) {
			return "", fmt.Errorf("%w.\n%s in ~/.commitronrc, or use a model with less verbose reasoning",
				err, generationBudgetHint(cfg, responseTokens))
		}
		return "", err
	}

	// Display that analysis is complete
	if cfg.UI.EnableTUI {
		DisplayAnalysisComplete()
	}

	// Debug: Show the raw response from the AI
	debugPrint(cfg, "AI RESPONSE", rawResponse)

	if strings.TrimSpace(rawResponse) == "" {
		return "", fmt.Errorf("AI returned an empty response; no commit message was generated")
	}

	// Parse the response into a structured CommitMessage
	commitMsg, err := ParseCommitMessageJSON(rawResponse)
	if err != nil {
		debugPrint(cfg, "PARSING ERROR", err.Error())
		// For conventional commits, ensure we have at least a type
		if cfg.Commit.Convention == config.ConventionalCommits {
			// If parsing failed but we can extract something useful from the raw text
			if strings.Contains(rawResponse, ": ") {
				parts := strings.SplitN(rawResponse, ": ", 2)
				if len(parts) == 2 {
					potential_type := strings.TrimSpace(parts[0])
					// Check if this could be a valid type
					if isValidCommitType(potential_type) {
						commitMsg.Type = potential_type
						commitMsg.Subject = strings.TrimSpace(parts[1])
						// Use the rest as body if applicable
						if cfg.Commit.IncludeBody && strings.Contains(commitMsg.Subject, "\n\n") {
							bodyParts := strings.SplitN(commitMsg.Subject, "\n\n", 2)
							if len(bodyParts) == 2 {
								commitMsg.Subject = bodyParts[0]
								commitMsg.Body = bodyParts[1]
							}
						}
						debugPrint(cfg, "MANUAL PARSING SUCCESSFUL", commitMsg)
					} else {
						// Default to a generic type
						commitMsg.Type = "chore"
						commitMsg.Subject = rawResponse
					}
				}
			} else {
				commitMsg.Type = "chore"
				commitMsg.Subject = rawResponse
			}
		} else {
			return rawResponse, nil // Fall back to raw response if parsing fails for non-conventional format
		}
	}

	// Never write a commit whose subject the model did not supply. Every recovery path
	// above defaults a type, so without this check a response that yielded no subject
	// would be committed as a bare "chore:".
	if strings.TrimSpace(commitMsg.Subject) == "" {
		return "", fmt.Errorf("could not extract a commit subject from the model response; nothing was committed")
	}

	// Debug: Show the parsed commit message
	debugPrint(cfg, "PARSED COMMIT", commitMsg)

	// A missing body is left missing. Substituting boilerplate like "Update N files
	// with necessary changes" is worse than a subject-only commit: it reads as though
	// it describes the change while saying nothing.
	if cfg.Commit.IncludeBody && strings.TrimSpace(commitMsg.Body) == "" {
		debugPrint(cfg, "NO BODY RETURNED", "model returned no body; committing subject only")
	}

	// Normalize the message to the configured convention. Every step here is
	// conservative: it repairs form (casing, stray punctuation, a missing type) but
	// never discards content the model wrote, because the model saw the diff and this
	// code did not. Earlier revisions rewrote or blanked bodies that tripped keyword
	// heuristics, which turned good messages into "Update N files with necessary changes".
	if cfg.Commit.Convention == config.ConventionalCommits {
		commitMsg = normalizeConventionalCommit(commitMsg)
	}

	commitMsg.Subject = fitSubject(commitMsg, cfg)

	if cfg.Commit.IncludeBody && cfg.Commit.MaxBodyLength > 0 && len(commitMsg.Body) > cfg.Commit.MaxBodyLength {
		commitMsg.Body = trimBodyToBullets(commitMsg.Body, cfg.Commit.MaxBodyLength)
		debugPrint(cfg, "TRIMMED BODY", commitMsg.Body)
	}

	if cfg.Commit.Convention == config.ConventionalCommits {
		if err := validateConventionalCommit(commitMsg, cfg); err != nil {
			// The message is still used: a soft-rule violation is worth a warning, not
			// a silent rewrite into something less informative.
			debugPrint(cfg, "CONVENTIONAL COMMIT VALIDATION WARNING", err.Error())
		}
	}

	// Format the message according to the configuration
	formattedMessage := FormatCommitMessage(commitMsg, cfg)

	// Debug: Show the final formatted message
	debugPrint(cfg, "FINAL COMMIT MESSAGE", formattedMessage)

	// Display the commit message but skip confirmation - auto-commit
	if cfg.UI.EnableTUI {
		fmt.Println("\n\033[1;36m💬 Generated Commit Message\033[0m")
		fmt.Println("\033[38;5;244m────────────────────────\033[0m")

		// Display the commit message with proper formatting
		lines := strings.Split(formattedMessage, "\n")
		for _, line := range lines {
			if line == "" {
				fmt.Println()
			} else {
				fmt.Printf("   %s\n", line)
			}
		}
		fmt.Println("\033[38;5;244m────────────────────────\033[0m")
	}

	return formattedMessage, nil
}

// buildPrompt creates a prompt for the AI based on the configuration using JSON templates
func buildPrompt(cfg *config.Config, files []string, changes string) string {
	// Debug which template is being used
	if cfg.AI.Debug {
		templateType := "Basic template"
		switch cfg.Commit.Convention {
		case config.ConventionalCommits:
			templateType = "Conventional commits template"
		case config.CustomConvention:
			templateType = "Custom template: " + cfg.Commit.CustomTemplate
		}
		debugPrint(cfg, "TEMPLATE TYPE", templateType)
	}

	// Serialize files list to JSON
	filesJSON, _ := json.Marshal(files)

	// Extract the most important changes from the diff if it's in our enhanced format
	if strings.Contains(changes, "# Summary of changes") || strings.Contains(changes, "diff --git") {
		// Prioritize the actual diff content and remove unnecessary headers
		enhancedChanges := extractKeyDiffContent(changes)
		if enhancedChanges != "" {
			changes = enhancedChanges
			if cfg.AI.Debug {
				debugPrint(cfg, "USING ENHANCED DIFF", "Using extracted key diff content")
			}
		}
	}

	// Token-aware truncation (this is a secondary check; main truncation happens in GenerateCommitMessage)
	tokenizerModel := cfg.Context.TokenizerModel
	if tokenizerModel == "" {
		tokenizerModel = cfg.AI.Model
	}

	originalTokens := tokenizer.CountTokens(changes, tokenizerModel)
	maxContextTokens := cfg.Context.MaxInputTokens
	if maxContextTokens == 0 {
		maxContextTokens = 100000
	}

	if originalTokens > maxContextTokens {
		changes = tokenizer.TruncateToTokenLimit(changes, maxContextTokens, tokenizerModel)
		if cfg.AI.Debug {
			newTokens := tokenizer.CountTokens(changes, tokenizerModel)
			debugPrint(cfg, "TRUNCATED", fmt.Sprintf("%d → %d tokens", originalTokens, newTokens))
		}
	}

	// Escape changes for JSON
	changesJSON, _ := json.Marshal(changes)

	// Determine if we want subject only based on config
	subjectOnly := !cfg.Commit.IncludeBody

	// Select template based on commit convention
	var template string
	switch cfg.Commit.Convention {
	case config.ConventionalCommits:
		template = fmt.Sprintf(
			ConventionalCommitsJSON,
			cfg.Commit.MaxLength,
			cfg.Commit.MaxBodyLength,
			cfg.Commit.IncludeBody,
			cfg.Commit.IncludeBody, // Pass include_body value to body_required field
			string(filesJSON),
			string(changesJSON),
			subjectOnly,
		)
	case config.CustomConvention:
		template = fmt.Sprintf(
			CustomCommitJSON,
			cfg.Commit.CustomTemplate,
			cfg.Commit.MaxLength,
			cfg.Commit.MaxBodyLength,
			cfg.Commit.IncludeBody,
			string(filesJSON),
			string(changesJSON),
			subjectOnly,
		)
	default:
		template = fmt.Sprintf(
			BaseTemplateJSON,
			cfg.Commit.MaxLength,
			cfg.Commit.MaxBodyLength,
			cfg.Commit.IncludeBody,
			string(filesJSON),
			string(changesJSON),
			subjectOnly,
		)
	}

	// Check if we have a custom system prompt
	hasCustomPrompt := cfg.AI.SystemPrompt != ""

	// Only add specific formatting instructions if no custom system prompt
	if !hasCustomPrompt {
		// Add explicit instructions to return ONLY valid JSON
		bodyInstructions := ""
		if cfg.Commit.IncludeBody {
			bodyInstructions = "YOU MUST INCLUDE A BODY formatted as a bulleted list, with every bullet starting with '- '. Include one bullet per meaningful change and cover ALL significant changes across the diff - more files/areas touched means more bullets. Each bullet must be specific and technical about what actually changed (name the component, function, or behavior). DO NOT include line statistics or formatting details like '+X/-Y lines'. DO NOT include raw metadata from the diff. NO marketing language or fluffy descriptions. "
		} else {
			bodyInstructions = "DO NOT include a body. "
		}

		conventionalRulesInstructions := ""
		if cfg.Commit.Convention == config.ConventionalCommits {
			conventionalRulesInstructions = "You MUST follow these conventional commit rules:\n" + ConventionalCommitRules + "\n"
			conventionalRulesInstructions += fmt.Sprintf("\nCRITICAL: The TOTAL length of 'type(scope): subject' MUST be under %d characters.\nExamples of good length: 'fix: update validation logic', 'feat(auth): add login timeout'\n", cfg.Commit.MaxLength)
			conventionalRulesInstructions += "\nALWAYS start your response with a valid type. NEVER start with just a colon.\n"
			conventionalRulesInstructions += "CORRECT: 'feat: add feature'\nINCORRECT: ': add feature'\n"
			conventionalRulesInstructions += "\nSTRICT REQUIREMENTS:\n"
			conventionalRulesInstructions += "1. Type MUST be one of: feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert\n"
			conventionalRulesInstructions += "2. Type MUST be lowercase\n"
			conventionalRulesInstructions += "3. Subject MUST be lowercase and not end with a period\n"
			conventionalRulesInstructions += "4. Scope (if used) MUST be lowercase and not contain spaces or special characters\n"
			conventionalRulesInstructions += "5. Body MUST be separated from subject by a blank line\n"
			conventionalRulesInstructions += "6. Body MUST be meaningful and explain what changes were made and why\n"
		}

		return "Your task is to create a CONCISE commit message based on the specifications below. " +
			"EXTREMELY IMPORTANT: Return ONLY a valid JSON object with no explanatory text. " +
			bodyInstructions +
			conventionalRulesInstructions +
			"DO NOT include any natural language explanation, introduction, or conclusion. " +
			"Return JUST the JSON object and nothing else. " +
			"IMPORTANT: Focus on the actual code changes in the diff and what they accomplish. Be BRIEF and CONCISE. " +
			fmt.Sprintf("CRITICAL: Ensure total commit subject length is UNDER %d characters.\n", cfg.Commit.MaxLength) +
			"Format:\n\n" +
			"For conventional commits, use this exact structure:\n" +
			"{\n" +
			"  \"type\": \"feat\", // One of: feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert\n" +
			"  \"scope\": \"optional scope\", // Optional, must be lowercase\n" +
			"  \"subject\": \"concise subject line\", // Must be lowercase, no period\n" +
			"  \"body\": \"" + bodyExample(cfg.Commit.IncludeBody) + "\"\n" +
			"}\n\n" +
			"Here are the specifications:\n\n" + template
	} else {
		// With custom system prompt, just provide the template data
		return "Generate a commit message based on this specification:\n\n" + template
	}
}

// extractKeyDiffContent focuses on the most important parts of the diff using smart summarization
func extractKeyDiffContent(diff string) string {
	// Use new smart summarization
	fileDiffs := ParseDiffByFile(diff)
	if len(fileDiffs) == 0 {
		// Fallback to old behavior if parsing fails
		lines := strings.Split(diff, "\n")
		var result []string
		inActualDiff := false

		for _, line := range lines {
			// Skip summary and header sections
			if strings.HasPrefix(line, "# ") || strings.HasPrefix(line, "Summary of changes") {
				continue
			}

			// Detect start of actual diff content
			if strings.HasPrefix(line, "diff --git") {
				inActualDiff = true
			}

			if inActualDiff {
				result = append(result, line)
			}
		}

		if len(result) == 0 {
			return diff
		}

		return strings.Join(result, "\n")
	}

	// Generate summaries for all files
	var summaries []string
	for _, fd := range fileDiffs {
		summary := stubFileSummary(fd)
		summaries = append(summaries, summary)
	}

	return strings.Join(summaries, "\n\n")
}

// bodyExample returns the appropriate body example text based on whether body is included
func bodyExample(includeBody bool) string {
	if includeBody {
		return "- Added validation to ensure commit messages follow the conventional commit format\\n- Improved error handling for malformed messages\\n- Added automatic truncation of overly long subject lines"
	}
	return "leave empty"
}

// generateWithOpenAI uses OpenAI to generate a commit message
// Helper function to get system prompt
func getSystemPrompt(cfg *config.Config) string {
	// If custom system prompt is provided, use it
	if cfg.AI.SystemPrompt != "" {
		return cfg.AI.SystemPrompt
	}

	// The format rules live in the user message, next to the diff they apply to.
	// Repeating them here would double their token cost and give the model two copies
	// to reconcile; the system prompt only sets the role and the output contract.
	return "You are a senior software engineer writing the commit message for a change " +
		"you just made. You read a diff and explain what the change does and why, " +
		"accurately and specifically, never inventing anything the diff does not show. " +
		"You reply with the commit message and nothing else."
}

// debugPrint prints debug information if debug mode is enabled
func debugPrint(cfg *config.Config, message string, data interface{}) {
	if !cfg.AI.Debug {
		return
	}

	// Create a debug marker for visibility
	debugMarker := "\n==== COMMITRON DEBUG ====\n"

	// Format the data based on its type
	var formattedData string
	switch v := data.(type) {
	case string:
		formattedData = v
	case []byte:
		formattedData = string(v)
	default:
		if data != nil {
			jsonData, err := json.MarshalIndent(data, "", "  ")
			if err == nil {
				formattedData = string(jsonData)
			} else {
				formattedData = fmt.Sprintf("%+v", data)
			}
		}
	}

	// Print the debug information
	fmt.Printf("%s%s:\n%s\n%s\n",
		debugMarker,
		message,
		formattedData,
		strings.Repeat("=", len(debugMarker)-1))
}

// GatherEnhancedFileInfo collects detailed information about the changed files
func GatherEnhancedFileInfo(cfg *config.Config, files []string) ([]EnhancedFileInfo, error) {
	var fileInfos []EnhancedFileInfo

	for _, file := range files {
		info := EnhancedFileInfo{
			Path: file,
		}

		// Get file extension for file type
		info.FileType = strings.TrimPrefix(filepath.Ext(file), ".")
		if info.FileType == "" {
			// Try to determine file type from the path or name
			if strings.Contains(file, "Dockerfile") {
				info.FileType = "dockerfile"
			} else if strings.Contains(file, "Makefile") {
				info.FileType = "makefile"
			} else if strings.HasPrefix(filepath.Base(file), ".") {
				// Config files that start with dot
				info.FileType = "config"
			} else {
				info.FileType = "unknown"
			}
		}

		// Get stats about line changes if enabled
		if cfg.Context.IncludeFileStats {
			// Use git diff --numstat to get line changes
			cmd := exec.Command("git", "diff", "--staged", "--numstat", "--", file)
			output, err := cmd.Output()
			if err == nil {
				// Parse the numstat output (format: <added> <removed> <file>)
				parts := strings.Fields(string(output))
				if len(parts) >= 2 {
					// Extract added/removed counts, ignoring binary files (shown as "-")
					if parts[0] != "-" {
						fmt.Sscanf(parts[0], "%d", &info.AddedLines)
					}
					if parts[1] != "-" {
						fmt.Sscanf(parts[1], "%d", &info.RemovedLines)
					}

					// Calculate percentage of file changed
					if info.AddedLines > 0 || info.RemovedLines > 0 {
						// Get total lines in file
						cmd = exec.Command("wc", "-l", file)
						wcOutput, err := cmd.Output()
						if err == nil {
							var totalLines int
							fmt.Sscanf(string(wcOutput), "%d", &totalLines)
							if totalLines > 0 {
								changePercentage := float64(info.AddedLines+info.RemovedLines) / float64(totalLines) * 100
								info.PercentageChange = fmt.Sprintf("%.1f%%", changePercentage)
							}
						}
					}
				}
			}
		}

		// Get file summary if enabled
		if cfg.Context.IncludeFileSummaries {
			// Read the first few lines to generate a summary
			cmd := exec.Command("head", "-n", "10", file)
			output, err := cmd.Output()
			if err == nil {
				lines := strings.Split(string(output), "\n")
				// Try to find a comment that might describe the file
				for _, line := range lines {
					line = strings.TrimSpace(line)
					// Look for comments that might be descriptive
					if (strings.HasPrefix(line, "//") ||
						strings.HasPrefix(line, "#") ||
						strings.HasPrefix(line, "/*") ||
						strings.HasPrefix(line, "<!--")) &&
						len(line) > 5 {
						// Found a likely descriptive comment
						info.Summary = strings.TrimSpace(strings.Trim(strings.Trim(strings.TrimSpace(line), "//"), "#*/<!- "))
						break
					}
				}

				// If we didn't find a descriptive comment, summarize based on file type
				if info.Summary == "" {
					switch info.FileType {
					case "go":
						// Try to extract package and maybe a struct/function name
						for _, line := range lines {
							if strings.HasPrefix(line, "package ") {
								packageName := strings.TrimSpace(strings.TrimPrefix(line, "package "))
								info.Summary = fmt.Sprintf("Go package %s", packageName)
								break
							}
						}
					case "js", "ts", "jsx", "tsx":
						// Look for imports, exports or component definitions
						if strings.Contains(string(output), "import ") && strings.Contains(string(output), "export ") {
							info.Summary = "JavaScript/TypeScript module with imports and exports"
						} else if strings.Contains(string(output), "function ") || strings.Contains(string(output), "class ") {
							info.Summary = "JavaScript/TypeScript file with functions or classes"
						}
					case "md", "markdown":
						// Extract first heading
						for _, line := range lines {
							if strings.HasPrefix(line, "# ") {
								info.Summary = fmt.Sprintf("Documentation: %s", strings.TrimSpace(strings.TrimPrefix(line, "# ")))
								break
							}
						}
						if info.Summary == "" {
							info.Summary = "Documentation file"
						}
					case "yaml", "yml":
						info.Summary = "YAML configuration file"
					case "json":
						info.Summary = "JSON data or configuration file"
					case "sh", "bash":
						info.Summary = "Shell script"
					case "dockerfile":
						info.Summary = "Docker container definition"
					case "makefile":
						info.Summary = "Make build configuration"
					}
				}

				// If still no summary, provide a generic one based on extension
				if info.Summary == "" {
					if info.FileType != "unknown" {
						info.Summary = fmt.Sprintf("%s file", strings.ToUpper(info.FileType))
					} else {
						info.Summary = "File with unknown type"
					}
				}
			}
		}

		// Get first N lines if enabled
		if cfg.Context.ShowFirstLinesOfFile > 0 {
			cmd := exec.Command("head", "-n", fmt.Sprintf("%d", cfg.Context.ShowFirstLinesOfFile), file)
			output, err := cmd.Output()
			if err == nil {
				info.FirstLines = string(output)
			}
		}

		fileInfos = append(fileInfos, info)
	}

	return fileInfos, nil
}

// GetRepoStructure returns a high-level overview of the repository structure
func GetRepoStructure(cfg *config.Config) (string, error) {
	if !cfg.Context.IncludeRepoStructure {
		return "", nil
	}

	// Use find with limited depth to get directory structure
	cmd := exec.Command("find", ".", "-type", "d", "-not", "-path", "*/\\.*", "-maxdepth", "2")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	// Process the output to create a structured overview
	var result strings.Builder
	result.WriteString("Repository structure:\n")

	dirs := strings.Split(strings.TrimSpace(string(output)), "\n")
	for _, dir := range dirs {
		if dir == "." {
			continue // Skip root
		}

		// Count files in directory (using separate commands since pipes aren't directly supported)
		findCmd := exec.Command("find", dir, "-type", "f", "-not", "-path", "*/\\.*", "-maxdepth", "1")
		findOutput, err := findCmd.Output()
		fileCount := "?"
		if err == nil {
			// Count the lines in the output
			lines := strings.Split(strings.TrimSpace(string(findOutput)), "\n")
			if len(lines) == 1 && lines[0] == "" {
				fileCount = "0"
			} else {
				fileCount = fmt.Sprintf("%d", len(lines))
			}
		}

		// Indent based on directory depth
		indentation := strings.Count(dir, "/")
		prefix := strings.Repeat("  ", indentation)
		dirName := filepath.Base(dir)

		result.WriteString(fmt.Sprintf("%s- %s/ (%s files)\n", prefix, dirName, fileCount))
	}

	return result.String(), nil
}

// validateConventionalCommit checks if a commit message follows conventional commit rules
func validateConventionalCommit(msg CommitMessage, cfg *config.Config) error {
	// Check if type is one of the allowed types
	allowedTypes := map[string]bool{
		"feat":     true,
		"fix":      true,
		"docs":     true,
		"style":    true,
		"refactor": true,
		"perf":     true,
		"test":     true,
		"build":    true,
		"ci":       true,
		"chore":    true,
		"revert":   true,
	}

	// Type is required and must be one of the allowed types
	if msg.Type == "" {
		return fmt.Errorf("commit type is required for conventional commits")
	}

	// Validate type is lowercase
	if msg.Type != strings.ToLower(msg.Type) {
		return fmt.Errorf("commit type must be lowercase: %s", msg.Type)
	}

	// Check if type is allowed
	if !allowedTypes[msg.Type] {
		return fmt.Errorf("commit type '%s' is not allowed for conventional commits; must be one of: feat, fix, docs, style, refactor, perf, test, build, ci, chore, revert", msg.Type)
	}

	// Subject is required
	if msg.Subject == "" {
		return fmt.Errorf("commit subject is required for conventional commits")
	}

	// Subject should not end with a period
	if strings.HasSuffix(msg.Subject, ".") {
		return fmt.Errorf("commit subject should not end with a period")
	}

	// Subject first letter should not be capitalized (conventional)
	if len(msg.Subject) > 0 && unicode.IsUpper([]rune(msg.Subject)[0]) {
		return fmt.Errorf("commit subject should not start with a capital letter")
	}

	// Subject should not contain newlines
	if strings.Contains(msg.Subject, "\n") {
		return fmt.Errorf("commit subject should not contain newlines")
	}

	// Subject should not be too generic
	genericSubjects := map[string]bool{
		"update": true,
		"fix":    true,
		"change": true,
		"modify": true,
		"add":    true,
		"remove": true,
		"delete": true,
	}

	if genericSubjects[strings.ToLower(msg.Subject)] {
		return fmt.Errorf("commit subject is too generic, please be more specific about what was changed")
	}

	// Body is required if configured
	if cfg.Commit.IncludeBody {
		trimmedBody := strings.TrimSpace(msg.Body)
		if trimmedBody == "" {
			return fmt.Errorf("commit body is required for conventional commits when include_body is true")
		}

		// Check if body is just placeholder text
		if strings.Contains(strings.ToLower(trimmedBody), "<descriptive body") ||
			strings.Contains(strings.ToLower(trimmedBody), "<optional body>") ||
			strings.Contains(strings.ToLower(trimmedBody), "<commit message>") {
			return fmt.Errorf("commit body contains placeholder text and needs to be replaced with actual content")
		}

		// Ensure body has reasonable length
		if len(trimmedBody) < 10 {
			return fmt.Errorf("commit body is too short (must be at least 10 characters)")
		}

		// Ensure body is separated from subject by a blank line
		if !strings.Contains(msg.Body, "\n\n") {
			return fmt.Errorf("commit body must be separated from subject by a blank line")
		}

		// Check for common issues in body
		if strings.Contains(strings.ToLower(trimmedBody), "this code") ||
			strings.Contains(strings.ToLower(trimmedBody), "the changes") ||
			strings.Contains(strings.ToLower(trimmedBody), "this commit") {
			return fmt.Errorf("commit body should not start with phrases like 'this code', 'the changes', or 'this commit'")
		}

		// Ensure body is not just a list of files
		if strings.Contains(trimmedBody, "file:") || strings.Contains(trimmedBody, "files:") {
			return fmt.Errorf("commit body should not be a list of files, focus on what changed and why")
		}
	}

	// Validate scope format if present
	if msg.Scope != "" {
		// Scope should be lowercase
		if msg.Scope != strings.ToLower(msg.Scope) {
			return fmt.Errorf("commit scope must be lowercase: %s", msg.Scope)
		}

		// Scope should not contain spaces
		if strings.Contains(msg.Scope, " ") {
			return fmt.Errorf("commit scope should not contain spaces")
		}

		// Scope should not contain special characters
		if strings.ContainsAny(msg.Scope, "!@#$%^&*()_+={}[]|\\:;\"'<>,.?/~`") {
			return fmt.Errorf("commit scope should not contain special characters")
		}

		// Scope should not be too generic
		if genericSubjects[strings.ToLower(msg.Scope)] {
			return fmt.Errorf("commit scope is too generic, please be more specific")
		}
	}

	return nil
}

// normalizeConventionalCommit repairs the *form* of a commit message without altering
// what it says: type casing and common misspellings, subject capitalization and stray
// trailing punctuation, scope casing. It deliberately does not rewrite wording or drop
// body lines - the model saw the diff, so its content is more trustworthy than any
// keyword heuristic here.
func normalizeConventionalCommit(msg CommitMessage) CommitMessage {
	msg.Type = strings.ToLower(strings.TrimSpace(msg.Type))

	typeCorrections := map[string]string{
		"feature":       "feat",
		"features":      "feat",
		"bugfix":        "fix",
		"bug":           "fix",
		"hotfix":        "fix",
		"document":      "docs",
		"documentation": "docs",
		"doc":           "docs",
		"styling":       "style",
		"refactoring":   "refactor",
		"performance":   "perf",
		"testing":       "test",
		"tests":         "test",
		"building":      "build",
		"maintenance":   "chore",
	}
	if corrected, ok := typeCorrections[msg.Type]; ok {
		msg.Type = corrected
	}
	if !isValidCommitType(msg.Type) {
		msg.Type = "chore"
	}

	msg.Subject = strings.TrimSpace(msg.Subject)
	msg.Subject = strings.TrimSuffix(msg.Subject, ".")

	// Lowercase the first letter only when it is not part of an identifier or acronym
	// ("API", "GetStagedFiles"): downcasing those would misname real symbols.
	if r := []rune(msg.Subject); len(r) > 0 && unicode.IsUpper(r[0]) {
		first := strings.Fields(msg.Subject)
		if len(first) > 0 && !hasInnerUpper(first[0]) {
			r[0] = unicode.ToLower(r[0])
			msg.Subject = string(r)
		}
	}

	if msg.Scope != "" {
		msg.Scope = strings.ToLower(strings.TrimSpace(msg.Scope))
	}

	msg.Body = strings.TrimSpace(msg.Body)
	return msg
}

// hasInnerUpper reports whether a word contains an uppercase letter after its first
// character, which marks it as an acronym or a camel-case identifier.
func hasInnerUpper(word string) bool {
	for _, r := range word[1:] {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// fitSubject shortens an over-long subject at a word boundary, preferring to drop a
// trailing clause over cutting mid-word or appending an ellipsis.
func fitSubject(msg CommitMessage, cfg *config.Config) string {
	prefix := 0
	if cfg.Commit.Convention == config.ConventionalCommits && msg.Type != "" {
		prefix = len(msg.Type) + 2 // "type: "
		if msg.Scope != "" {
			prefix += len(msg.Scope) + 2 // "(scope)"
		}
	}

	available := cfg.Commit.MaxLength - prefix
	if available <= 0 || len(msg.Subject) <= available {
		return msg.Subject
	}

	// Cut after the last clause separator or word boundary that still fits.
	cut := msg.Subject[:available]
	for _, sep := range []string{" - ", ", ", " and ", " "} {
		if i := strings.LastIndex(cut, sep); i > available/2 {
			return strings.TrimRight(msg.Subject[:i], " ,;-")
		}
	}
	return strings.TrimRight(cut, " ,;-")
}

// trimBodyToBullets shortens an over-long body by dropping whole trailing bullets
// rather than cutting mid-sentence, so every bullet that survives is still readable.
func trimBodyToBullets(body string, maxLen int) string {
	lines := strings.Split(strings.TrimSpace(body), "\n")
	var kept []string
	total := 0

	for _, line := range lines {
		cost := len(line) + 1
		if total+cost > maxLen {
			break
		}
		kept = append(kept, line)
		total += cost
	}

	if len(kept) == 0 {
		// A single bullet longer than the whole budget: cut it at a word boundary.
		if len(lines) > 0 && len(lines[0]) > maxLen && maxLen > 0 {
			cut := lines[0][:maxLen]
			if i := strings.LastIndex(cut, " "); i > 0 {
				return cut[:i]
			}
			return cut
		}
		return strings.TrimSpace(body)
	}

	return strings.Join(kept, "\n")
}

// isValidCommitType checks if a string is a valid conventional commit type
func isValidCommitType(t string) bool {
	validTypes := map[string]bool{
		"feat":     true,
		"fix":      true,
		"docs":     true,
		"style":    true,
		"refactor": true,
		"perf":     true,
		"test":     true,
		"build":    true,
		"ci":       true,
		"chore":    true,
		"revert":   true,
	}
	return validTypes[t]
}
