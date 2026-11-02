package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/johnstilia/commitron/pkg/config"
)

const (
	changedSubmodule = "libs/changed"
	cleanSubmodule   = "libs/clean"
)

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// newRepoWithSubmodules builds a committed parent repository with two submodules, and
// isolates git from the developer's own configuration.
func newRepoWithSubmodules(t *testing.T) string {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, role := range []string{"AUTHOR", "COMMITTER"} {
		t.Setenv("GIT_"+role+"_NAME", "test")
		t.Setenv("GIT_"+role+"_EMAIL", "test@example.invalid")
	}

	base := t.TempDir()
	upstream := filepath.Join(base, "upstream")
	runGit(t, base, "init", "-q", upstream)
	writeFile(t, filepath.Join(upstream, "lib.go"), "package lib\n")
	runGit(t, upstream, "add", ".")
	runGit(t, upstream, "commit", "-qm", "init library")

	root := filepath.Join(base, "root")
	runGit(t, base, "init", "-q", root)
	for _, submodule := range []string{changedSubmodule, cleanSubmodule} {
		runGit(t, root, "-c", "protocol.file.allow=always", "submodule", "--quiet", "add", upstream, submodule)
	}
	runGit(t, root, "commit", "-qm", "add submodules")
	t.Chdir(root)
	return root
}

func testConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Commit.AutoStageNewSource = true
	cfg.Commit.AutoCommitSubmodules = true
	return cfg
}

// recordingGenerator returns numbered messages and records the changes it was given.
func recordingGenerator(changes *[]string) messageGenerator {
	return func(_ *config.Config, _ []string, diff string) (string, error) {
		*changes = append(*changes, diff)
		return "commit " + string(rune('0'+len(*changes))), nil
	}
}

func TestCommitAll_CommitsChangedSubmoduleThenParent(t *testing.T) {
	root := newRepoWithSubmodules(t)
	changed := filepath.Join(root, changedSubmodule)
	clean := filepath.Join(root, cleanSubmodule)
	writeFile(t, filepath.Join(changed, "lib.go"), "package lib\n\nconst Retry = 3\n")
	writeFile(t, filepath.Join(changed, "new.go"), "package lib\n")
	cleanHead := runGit(t, clean, "rev-parse", "HEAD")

	var seen []string
	if err := commitAll(testConfig(), recordingGenerator(&seen)); err != nil {
		t.Fatal(err)
	}

	if len(seen) != 2 {
		t.Fatalf("expected one message for the submodule and one for the parent, got %d", len(seen))
	}
	if got := runGit(t, changed, "log", "-1", "--format=%s"); got != "commit 1" {
		t.Errorf("submodule commit: got %q", got)
	}
	if got := runGit(t, changed, "show", "--name-only", "--format=", "HEAD"); !strings.Contains(got, "new.go") {
		t.Errorf("new source file was not committed in the submodule: %q", got)
	}
	if got := runGit(t, clean, "rev-parse", "HEAD"); got != cleanHead {
		t.Error("the clean submodule should not get a commit")
	}
	if got := runGit(t, root, "log", "-1", "--format=%s"); got != "commit 2" {
		t.Errorf("parent commit: got %q", got)
	}
	if got := runGit(t, root, "show", "--name-only", "--format=", "HEAD"); got != changedSubmodule {
		t.Errorf("parent should record only the submodule update, got %q", got)
	}
	if !strings.Contains(seen[1], "commit 1") {
		t.Errorf("the parent's input should name the submodule's commit:\n%s", seen[1])
	}
	if got := runGit(t, root, "status", "--porcelain"); got != "" {
		t.Errorf("expected a clean tree, got:\n%s", got)
	}
}

func TestCommitAll_FromParentSubdirectory(t *testing.T) {
	root := newRepoWithSubmodules(t)
	writeFile(t, filepath.Join(root, changedSubmodule, "lib.go"), "package lib\n\nconst Retry = 3\n")
	t.Chdir(filepath.Join(root, "libs"))

	var seen []string
	if err := commitAll(testConfig(), recordingGenerator(&seen)); err != nil {
		t.Fatal(err)
	}

	if got := runGit(t, root, "status", "--porcelain"); got != "" {
		t.Errorf("expected a clean tree, got:\n%s", got)
	}
	if wd, _ := os.Getwd(); filepath.Base(wd) != "libs" {
		t.Errorf("working directory was not restored: %s", wd)
	}
}

func TestCommitAll_NoChangesAnywhereIsAnError(t *testing.T) {
	newRepoWithSubmodules(t)

	var seen []string
	if err := commitAll(testConfig(), recordingGenerator(&seen)); err == nil {
		t.Error("expected an error when nothing changed")
	}
	if len(seen) != 0 {
		t.Errorf("no message should be generated, got %d", len(seen))
	}
}

func TestCommitAll_SkipsDetachedSubmodule(t *testing.T) {
	root := newRepoWithSubmodules(t)
	changed := filepath.Join(root, changedSubmodule)
	runGit(t, changed, "checkout", "-q", "--detach")
	writeFile(t, filepath.Join(changed, "lib.go"), "package lib\n\nconst Retry = 3\n")
	writeFile(t, filepath.Join(root, "root.go"), "package root\n")
	head := runGit(t, changed, "rev-parse", "HEAD")

	var seen []string
	if err := commitAll(testConfig(), recordingGenerator(&seen)); err != nil {
		t.Fatal(err)
	}

	if got := runGit(t, changed, "rev-parse", "HEAD"); got != head {
		t.Error("a detached submodule should not get a commit")
	}
	if got := runGit(t, changed, "status", "--porcelain"); !strings.HasPrefix(got, "M lib.go") && !strings.HasPrefix(got, "M  lib.go") {
		t.Errorf("the change should stay uncommitted and unstaged, got %q", got)
	}
	if len(seen) != 1 {
		t.Errorf("only the parent should be committed, got %d messages", len(seen))
	}
}

func TestCommitAll_SubmoduleFailureLeavesParentUncommitted(t *testing.T) {
	root := newRepoWithSubmodules(t)
	writeFile(t, filepath.Join(root, changedSubmodule, "lib.go"), "package lib\n\nconst Retry = 3\n")
	writeFile(t, filepath.Join(root, "root.go"), "package root\n")
	head := runGit(t, root, "rev-parse", "HEAD")

	failure := errors.New("model unavailable")
	err := commitAll(testConfig(), func(*config.Config, []string, string) (string, error) {
		return "", failure
	})

	if !errors.Is(err, failure) {
		t.Errorf("expected the generation failure, got %v", err)
	}
	if got := runGit(t, root, "rev-parse", "HEAD"); got != head {
		t.Error("the parent should not be committed after a submodule failure")
	}
}

func TestCommitAll_SubmodulesCanBeDisabled(t *testing.T) {
	root := newRepoWithSubmodules(t)
	changed := filepath.Join(root, changedSubmodule)
	writeFile(t, filepath.Join(changed, "lib.go"), "package lib\n\nconst Retry = 3\n")
	head := runGit(t, changed, "rev-parse", "HEAD")
	cfg := testConfig()
	cfg.Commit.AutoCommitSubmodules = false

	var seen []string
	if err := commitAll(cfg, recordingGenerator(&seen)); err == nil {
		t.Error("with submodules disabled, a clean parent has nothing to commit")
	}
	if got := runGit(t, changed, "rev-parse", "HEAD"); got != head {
		t.Error("the submodule should not be committed when disabled")
	}
}

func TestCommitAll_DryRunCreatesNoCommits(t *testing.T) {
	root := newRepoWithSubmodules(t)
	changed := filepath.Join(root, changedSubmodule)
	writeFile(t, filepath.Join(changed, "lib.go"), "package lib\n\nconst Retry = 3\n")
	head := runGit(t, changed, "rev-parse", "HEAD")
	dryRun = true
	t.Cleanup(func() { dryRun = false })

	var seen []string
	if err := commitAll(testConfig(), recordingGenerator(&seen)); err != nil {
		t.Fatalf("a submodule-only change should not report that nothing changed: %v", err)
	}
	if got := runGit(t, changed, "rev-parse", "HEAD"); got != head {
		t.Error("a dry run should not create a commit")
	}
}
