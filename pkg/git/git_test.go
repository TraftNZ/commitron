package git

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func runGit(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

func writeFiles(t *testing.T, root string, files ...string) {
	t.Helper()
	for _, file := range files {
		full := filepath.Join(root, file)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("content\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestIsSourceFile(t *testing.T) {
	cases := map[string]bool{
		"main.go":                true,
		"pkg/ai/client.GO":       true,
		"../sibling/app.py":      true,
		"Makefile":               true,
		"docker/Dockerfile":      true,
		".env":                   false,
		"secrets.pem":            false,
		"config.yaml":            false,
		"README.md":              false,
		"data/dump.bin":          false,
		"noextension":            false,
		"node_modules/lib/x.js":  false,
		"web/dist/bundle.js":     false,
		".claude/hooks/check.sh": false,
	}
	for file, want := range cases {
		if got := isSourceFile(file); got != want {
			t.Errorf("isSourceFile(%q) = %t, want %t", file, got, want)
		}
	}
}

func TestStageNewSourceFiles(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	runGit(t, "init", "-q")
	writeFiles(t, root,
		".gitignore", "cmd/main.go", "cmd/gl*b.go", "cmd/with space.go", "lib/util.py",
		"ignored.go", "notes.md", ".env",
	)
	if err := os.WriteFile(".gitignore", []byte("ignored.go\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// From a subdirectory, so the whole repository must still be covered.
	t.Chdir(filepath.Join(root, "cmd"))
	got, err := StageNewSourceFiles()
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"../lib/util.py", "gl*b.go", "main.go", "with space.go"}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Errorf("returned %q, want %q", got, want)
	}

	staged := strings.Split(strings.TrimSpace(runGit(t, "-c", "core.quotePath=false", "diff", "--cached", "--name-only", "--no-renames")), "\n")
	slices.Sort(staged)
	wantStaged := []string{"cmd/gl*b.go", "cmd/main.go", "cmd/with space.go", "lib/util.py"}
	if !slices.Equal(staged, wantStaged) {
		t.Errorf("staged %q, want %q", staged, wantStaged)
	}
}

func TestStageNewSourceFiles_NothingToStage(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)
	runGit(t, "init", "-q")
	writeFiles(t, root, "notes.md")

	got, err := StageNewSourceFiles()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected nothing staged, got %q", got)
	}
}

func TestListSubmodules_SkipsUninitialized(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.invalid")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.invalid")

	base := t.TempDir()
	upstream := filepath.Join(base, "upstream")
	root := filepath.Join(base, "root")
	runGit(t, "init", "-q", upstream)
	runGit(t, "-C", upstream, "commit", "-q", "--allow-empty", "-m", "init")
	runGit(t, "init", "-q", root)
	t.Chdir(root)
	for _, name := range []string{"libs/with space", "libs/gone"} {
		runGit(t, "-c", "protocol.file.allow=always", "submodule", "--quiet", "add", upstream, name)
	}
	runGit(t, "submodule", "--quiet", "deinit", "-f", "libs/gone")

	// From a subdirectory, so the paths must still resolve against the repository root.
	t.Chdir(filepath.Join(root, "libs"))
	got, err := ListSubmodules()
	if err != nil {
		t.Fatal(err)
	}

	want := []string{filepath.Join(root, "libs/with space")}
	if !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}
