package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// diffContextArg controls how many unchanged context lines surround each hunk in
// the staged diff. "-U0" keeps every added/removed line plus the hunk headers
// (which carry the enclosing function names) while dropping unchanged surrounding
// lines, cutting prompt size — and thus model prefill latency — with no loss of
// changed content.
const diffContextArg = "-U0"

// sourceExtensions lists the file types staged automatically when untracked. It is an
// allow list because a new file of unknown type may be a secret, a data dump or a build
// artifact that .gitignore missed; config formats are left out for the same reason.
var sourceExtensions = map[string]bool{
	".bash": true, ".bicep": true, ".c": true, ".cc": true, ".cjs": true, ".cpp": true,
	".cs": true, ".css": true, ".cxx": true, ".dart": true, ".ex": true, ".exs": true,
	".fish": true, ".go": true, ".h": true, ".hpp": true, ".html": true, ".java": true,
	".js": true, ".jsx": true, ".kt": true, ".kts": true, ".lua": true, ".m": true,
	".mjs": true, ".mm": true, ".php": true, ".pl": true, ".proto": true, ".ps1": true,
	".py": true, ".r": true, ".rb": true, ".rs": true, ".scala": true, ".scss": true,
	".sh": true, ".sql": true, ".svelte": true, ".swift": true, ".tf": true, ".ts": true,
	".tsx": true, ".vue": true, ".zig": true, ".zsh": true,
}

// sourceFileNames lists source files that are recognized by name, not extension.
var sourceFileNames = map[string]bool{"dockerfile": true, "makefile": true}

// generatedDirs holds directories whose contents are not hand-written source, for
// repositories whose .gitignore does not already exclude them.
var generatedDirs = map[string]bool{
	"__pycache__": true, "build": true, "dist": true, "node_modules": true,
	"out": true, "target": true, "vendor": true, "venv": true,
}

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
	// "--submodule=log" reports a submodule update as its commit subjects rather than
	// two opaque hashes, which is all the parent commit message has to go on.
	cmd := exec.Command("git", "diff", "--cached", diffContextArg, "--submodule=log")
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

// isSourceFile reports whether a slash-separated path looks like hand-written source
// code: a recognized file type outside generated and hidden directories.
func isSourceFile(filePath string) bool {
	segments := strings.Split(filePath, "/")
	for _, dir := range segments[:len(segments)-1] {
		if dir == ".." {
			continue
		}
		if generatedDirs[dir] || strings.HasPrefix(dir, ".") {
			return false
		}
	}

	name := strings.ToLower(segments[len(segments)-1])
	return sourceFileNames[name] || sourceExtensions[path.Ext(name)]
}

// StageNewSourceFiles stages untracked source files across the whole repository,
// honouring .gitignore, and returns the paths it staged relative to the current
// directory. Other untracked files are left alone.
func StageNewSourceFiles() ([]string, error) {
	// ":/" widens the listing to the repository root when run from a subdirectory, to
	// match "git add -u". NUL separation keeps unusual file names intact.
	var out, stderr bytes.Buffer
	list := exec.Command("git", "ls-files", "--others", "--exclude-standard", "-z", "--", ":/")
	list.Stdout = &out
	list.Stderr = &stderr
	if err := list.Run(); err != nil {
		return nil, fmt.Errorf("listing untracked files: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var files []string
	for _, file := range strings.Split(out.String(), "\x00") {
		if file != "" && isSourceFile(file) {
			files = append(files, file)
		}
	}
	if len(files) == 0 {
		return nil, nil
	}

	// Paths go over stdin so a large set cannot overflow the argument limit, and are
	// literal so a name containing "*" stages only that file.
	stderr.Reset()
	add := exec.Command("git", "--literal-pathspecs", "add", "--pathspec-from-file=-", "--pathspec-file-nul")
	add.Stdin = strings.NewReader(strings.Join(files, "\x00"))
	add.Stderr = &stderr
	if err := add.Run(); err != nil {
		return nil, fmt.Errorf("staging new source files: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return files, nil
}

// gitlinkMode is the index file mode git uses for a submodule entry.
const gitlinkMode = "160000"

// ListSubmodules returns the absolute paths of the current repository's checked-out
// submodules, one level deep. Submodules that were never initialized are left out.
func ListSubmodules() ([]string, error) {
	var root, stderr bytes.Buffer
	topLevel := exec.Command("git", "rev-parse", "--show-toplevel")
	topLevel.Stdout = &root
	topLevel.Stderr = &stderr
	if err := topLevel.Run(); err != nil {
		return nil, fmt.Errorf("finding repository root: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	rootDir := strings.TrimSpace(root.String())

	var out bytes.Buffer
	stderr.Reset()
	list := exec.Command("git", "ls-files", "--stage", "--full-name", "-z", "--", ":/")
	list.Stdout = &out
	list.Stderr = &stderr
	if err := list.Run(); err != nil {
		return nil, fmt.Errorf("listing submodules: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var submodules []string
	for _, entry := range strings.Split(out.String(), "\x00") {
		// Each entry is "<mode> <object> <stage>\t<path>".
		metadata, file, found := strings.Cut(entry, "\t")
		if !found || !strings.HasPrefix(metadata, gitlinkMode+" ") {
			continue
		}
		dir := filepath.Join(rootDir, file)
		// An initialized submodule has a .git entry; otherwise it is an empty directory.
		if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
			continue
		}
		submodules = append(submodules, dir)
	}
	// A conflicted submodule has one index entry per merge stage.
	return slices.Compact(submodules), nil
}

// IsDetachedHead reports whether the current repository's HEAD is not on a branch.
func IsDetachedHead() (bool, error) {
	err := exec.Command("git", "symbolic-ref", "--quiet", "HEAD").Run()
	if err == nil {
		return false, nil
	}
	// "symbolic-ref --quiet" exits 1 for a detached HEAD and differently for real errors.
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return true, nil
	}
	return false, err
}

// HasUncommittedChanges reports whether the working tree or index differs from HEAD,
// counting untracked files.
func HasUncommittedChanges() (bool, error) {
	out, err := exec.Command("git", "status", "--porcelain").Output()
	if err != nil {
		return false, err
	}
	return len(bytes.TrimSpace(out)) > 0, nil
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
