package git

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
)

// diffContextArg controls how many unchanged context lines surround each hunk in
// the staged diff. "-U0" keeps every added/removed line plus the hunk headers
// (which carry the enclosing function names) while dropping unchanged surrounding
// lines, cutting prompt size — and thus model prefill latency — with no loss of
// changed content.
const diffContextArg = "-U0"

// IsGitRepo checks if the current directory is a git repository
func IsGitRepo() bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	err := cmd.Run()
	return err == nil
}

// GetStagedFiles returns a list of staged files
func GetStagedFiles() ([]string, error) {
	cmd := exec.Command("git", "diff", "--name-only", "--cached")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return nil, err
	}

	files := strings.Split(strings.TrimSpace(out.String()), "\n")
	// Filter out empty strings in case there are no staged files
	var result []string
	for _, file := range files {
		if file != "" {
			result = append(result, file)
		}
	}

	return result, nil
}

// GetStagedChanges returns the diff of staged changes
func GetStagedChanges() (string, error) {
	cmd := exec.Command("git", "diff", "--cached", diffContextArg)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return "", err
	}

	return out.String(), nil
}

// GetModifiedFiles returns a list of tracked modified files (staged and unstaged, excludes untracked)
func GetModifiedFiles() ([]string, error) {
	// Use git diff --name-only HEAD to get only tracked files that have been modified
	// This excludes untracked files
	cmd := exec.Command("git", "diff", "--name-only", "HEAD")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return nil, err
	}

	files := strings.Split(strings.TrimSpace(out.String()), "\n")
	// Filter out empty strings
	var result []string
	for _, file := range files {
		if file != "" {
			result = append(result, file)
		}
	}

	return result, nil
}

// GetUnstagedFiles returns a list of tracked modified but unstaged files (excludes untracked)
func GetUnstagedFiles() ([]string, error) {
	// git diff --name-only only shows tracked files that have been modified
	// This excludes untracked files
	cmd := exec.Command("git", "diff", "--name-only")
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	if err != nil {
		return nil, err
	}

	files := strings.Split(strings.TrimSpace(out.String()), "\n")
	// Filter out empty strings
	var result []string
	for _, file := range files {
		if file != "" {
			result = append(result, file)
		}
	}

	return result, nil
}

// StageAllModified stages only tracked modified files (excludes untracked files)
func StageAllModified() error {
	// Get only modified tracked files (not untracked)
	cmd := exec.Command("git", "add", "-u")
	return cmd.Run()
}

// Commit creates a new commit with the given message
func Commit(message string) error {
	if message == "" {
		return errors.New("commit message cannot be empty")
	}

	// Write commit message to temporary file
	tmpFile, err := os.CreateTemp("", "commitron-msg-")
	if err != nil {
		return err
	}
	defer os.Remove(tmpFile.Name())

	_, err = tmpFile.WriteString(message)
	if err != nil {
		return err
	}

	err = tmpFile.Close()
	if err != nil {
		return err
	}

	// Create commit using the temp file
	cmd := exec.Command("git", "commit", "-F", tmpFile.Name())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}
