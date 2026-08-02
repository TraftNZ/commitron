package ai

import (
	"fmt"
	"strings"
	"testing"

	"github.com/johnstilia/commitron/pkg/tokenizer"
)

const testTokenizer = "gpt-4"

// makeDiff builds a synthetic unified diff with n files of linesPerFile changed lines.
func makeDiff(n, linesPerFile int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("pkg/feature/module_%02d.go", i)
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n", path, path)
		fmt.Fprintf(&b, "index 1a2b3c4..5d6e7f8 100644\n--- a/%s\n+++ b/%s\n", path, path)
		fmt.Fprintf(&b, "@@ -10,0 +11,%d @@ func Compute%02d(in int) int\n", linesPerFile, i)
		for j := 0; j < linesPerFile; j++ {
			fmt.Fprintf(&b, "+\tresult := compute_%02d_%02d(input, factor) // step %d\n", i, j, j)
		}
	}
	return b.String()
}

func TestCompactDiff_NeverExceedsBudget(t *testing.T) {
	diff := makeDiff(15, 30)

	// Budgets at and above the floor callers actually use must be honored exactly.
	for _, budget := range []int{20000, 8000, 4000, 2000, 1000, minDiffBudgetTokens} {
		out := CompactDiff(diff, budget, testTokenizer)
		if got := tokenizer.CountTokens(out, testTokenizer); got > budget {
			t.Errorf("budget %d: output is %d tokens, over budget", budget, got)
		}
	}
}

func TestCompactDiff_DegradesGracefullyBelowFloor(t *testing.T) {
	diff := makeDiff(15, 30)

	// Below the floor a single file path is the smallest truthful output; it may
	// exceed a nonsensically small budget, but must stay bounded and non-empty.
	for _, budget := range []int{200, 100, 50, 20, 5, 1, 0} {
		out := CompactDiff(diff, budget, testTokenizer)
		if strings.TrimSpace(out) == "" {
			t.Errorf("budget %d: produced empty output", budget)
		}
		if got := tokenizer.CountTokens(out, testTokenizer); got > 200 {
			t.Errorf("budget %d: %d tokens, far above the floor", budget, got)
		}
	}
}

func TestCompactDiff_ReducesLosslesslyWhenBudgetIsAmple(t *testing.T) {
	diff := makeDiff(10, 20)
	raw := tokenizer.CountTokens(diff, testTokenizer)

	out := CompactDiff(diff, 1_000_000, testTokenizer)
	got := tokenizer.CountTokens(out, testTokenizer)

	if got >= raw {
		t.Errorf("expected compaction to shrink the diff, got %d tokens from %d", got, raw)
	}
	// With an unlimited budget nothing should be elided.
	if strings.Contains(out, "elided") {
		t.Errorf("unexpected elision with an unlimited budget:\n%s", out)
	}
	// Every changed line must survive.
	for i := 0; i < 10; i++ {
		if !strings.Contains(out, fmt.Sprintf("compute_%02d_19", i)) {
			t.Errorf("file %d lost its last changed line", i)
		}
	}
}

func TestCompactDiff_KeepsEveryFileAndSymbol(t *testing.T) {
	diff := makeDiff(20, 60)

	// A budget far too small to hold the content, but the skeleton must survive.
	out := CompactDiff(diff, 3000, testTokenizer)

	for i := 0; i < 20; i++ {
		path := fmt.Sprintf("module_%02d.go", i)
		if !strings.Contains(out, path) {
			t.Errorf("file %s silently dropped from compacted diff", path)
		}
		symbol := fmt.Sprintf("Compute%02d", i)
		if !strings.Contains(out, symbol) {
			t.Errorf("hunk symbol %s dropped from compacted diff", symbol)
		}
	}
	if !strings.Contains(out, "elided") {
		t.Error("expected an explicit elision marker when content was dropped")
	}
}

func TestCompactDiff_StubsGeneratedAndBinaryFiles(t *testing.T) {
	diff := "diff --git a/go.sum b/go.sum\n--- a/go.sum\n+++ b/go.sum\n" +
		"@@ -1,3 +1,3 @@\n+github.com/foo/bar v1.2.3 h1:abcdefghijklmnop=\n" +
		"+github.com/foo/bar v1.2.3/go.mod h1:qrstuvwxyz=\n" +
		"diff --git a/logo.png b/logo.png\n" +
		"index 1111111..2222222 100644\nBinary files a/logo.png and b/logo.png differ\n"

	out := CompactDiff(diff, 10000, testTokenizer)

	if !strings.Contains(out, "generated/lockfile") {
		t.Errorf("go.sum should be stubbed as generated:\n%s", out)
	}
	if strings.Contains(out, "h1:abcdefghijklmnop") {
		t.Errorf("generated file contents should not reach the model:\n%s", out)
	}
	if !strings.Contains(out, "binary file") {
		t.Errorf("binary file should be stubbed:\n%s", out)
	}
}

func TestCompactDiff_DropsPlumbingAndContextLines(t *testing.T) {
	diff := "diff --git a/main.go b/main.go\n" +
		"index abc1234..def5678 100644\n--- a/main.go\n+++ b/main.go\n" +
		"@@ -5,7 +5,7 @@ func main()\n" +
		" unchanged context line\n" +
		"-old := compute()\n" +
		"+new := computeBetter()\n" +
		" another context line\n"

	out := CompactDiff(diff, 10000, testTokenizer)

	for _, banned := range []string{"index abc1234", "--- a/main.go", "+++ b/main.go", "unchanged context", "another context"} {
		if strings.Contains(out, banned) {
			t.Errorf("expected %q to be stripped, got:\n%s", banned, out)
		}
	}
	for _, wanted := range []string{"main.go", "func main()", "-old := compute()", "+new := computeBetter()"} {
		if !strings.Contains(out, wanted) {
			t.Errorf("expected %q to survive, got:\n%s", wanted, out)
		}
	}
}

func TestCompactDiff_CollapsesRepeatedHunkSymbols(t *testing.T) {
	var b strings.Builder
	b.WriteString("diff --git a/w.yml b/w.yml\n--- a/w.yml\n+++ b/w.yml\n")
	for i := 0; i < 8; i++ {
		fmt.Fprintf(&b, "@@ -%d +%d @@ jobs:\n+  step_%d: run\n", i, i, i)
	}

	out := CompactDiff(b.String(), 10000, testTokenizer)

	if n := strings.Count(out, "@ jobs:"); n != 1 {
		t.Errorf("expected the repeated hunk symbol to be printed once, got %d times:\n%s", n, out)
	}
	for i := 0; i < 8; i++ {
		if !strings.Contains(out, fmt.Sprintf("step_%d", i)) {
			t.Errorf("collapsing symbols must not drop content: step_%d missing", i)
		}
	}
}

func TestCompactDiff_BudgetSpreadsAcrossFiles(t *testing.T) {
	// One huge file next to several small ones: the small files must not be starved.
	diff := makeDiff(1, 400) + makeDiff(4, 5)

	out := CompactDiff(diff, 1500, testTokenizer)

	// makeDiff(4,5) regenerates module_00..03; module_00 collides with the huge file,
	// so assert on the distinct small ones.
	for i := 1; i < 4; i++ {
		if !strings.Contains(out, fmt.Sprintf("compute_%02d_04", i)) {
			t.Errorf("small file %d was starved by the large one:\n%s", i, out)
		}
	}
}

func TestAllocate_MaxMinFairness(t *testing.T) {
	tests := []struct {
		name    string
		demands []int
		total   int
		want    []int
	}{
		{"all fit", []int{10, 20, 30}, 100, []int{10, 20, 30}},
		{"even split when all oversized", []int{100, 100, 100}, 30, []int{10, 10, 10}},
		{"surplus flows to the greedy", []int{2, 2, 100}, 30, []int{2, 2, 26}},
		{"nothing to give", []int{5, 5}, 0, []int{0, 0}},
		{"negative total", []int{5, 5}, -10, []int{0, 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := allocate(tt.demands, tt.total)
			sum := 0
			for i, g := range got {
				if g > tt.demands[i] {
					t.Errorf("share %d exceeds demand: %d > %d", i, g, tt.demands[i])
				}
				sum += g
			}
			if tt.total > 0 && sum > tt.total {
				t.Errorf("allocated %d, over total %d", sum, tt.total)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("got %v, want %v", got, tt.want)
					break
				}
			}
		})
	}
}

func TestCompactDiff_EmptyAndUnparseableInput(t *testing.T) {
	if out := CompactDiff("", 1000, testTokenizer); out != "" {
		t.Errorf("empty diff should stay empty, got %q", out)
	}
	garbage := "this is not a diff at all"
	if out := CompactDiff(garbage, 1000, testTokenizer); out != garbage {
		t.Errorf("unparseable input should pass through unchanged, got %q", out)
	}
}
