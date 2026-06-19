package ai

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/johnstilia/commitron/pkg/config"
	"github.com/johnstilia/commitron/pkg/tokenizer"
)

// withFakeLLM swaps summarizeCall for the duration of a test and restores it afterwards.
func withFakeLLM(t *testing.T, fn func(cfg *config.Config, sys, user string) (string, error)) {
	t.Helper()
	orig := summarizeCall
	summarizeCall = fn
	t.Cleanup(func() { summarizeCall = orig })
}

func testConfig() *config.Config {
	cfg := config.DefaultConfig()
	cfg.AI.Provider = config.OpenAI
	cfg.AI.Model = "gpt-4"
	cfg.Context.TokenizerModel = "gpt-4"
	return cfg
}

// keepRatioCompressor returns a fake LLM that always preserves any line referencing a file
// path/header and keeps only `ratio` of the remaining lines, guaranteeing output < input
// while file attribution survives.
func keepRatioCompressor(ratio float64) func(*config.Config, string, string) (string, error) {
	return func(_ *config.Config, _ string, user string) (string, error) {
		// userPrompt embeds the content after a blank line; operate on the whole thing.
		lines := strings.Split(user, "\n")
		var pathLines, other []string
		for _, l := range lines {
			if strings.Contains(l, "diff --git") || strings.Contains(l, ".go") && strings.Contains(l, "/") {
				pathLines = append(pathLines, l)
			} else if strings.TrimSpace(l) != "" {
				other = append(other, l)
			}
		}
		keep := int(float64(len(other)) * ratio)
		out := append([]string{}, pathLines...)
		out = append(out, other[:keep]...)
		return strings.TrimSpace(strings.Join(out, "\n")), nil
	}
}

// makeLargeDiff builds a synthetic multi-file git diff with `n` Go files of `linesPerFile`
// added lines each.
func makeLargeDiff(n, linesPerFile int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		path := fmt.Sprintf("pkg/feature/module_%02d.go", i)
		b.WriteString(fmt.Sprintf("diff --git a/%s b/%s\n", path, path))
		b.WriteString("new file mode 100644\n")
		b.WriteString(fmt.Sprintf("--- /dev/null\n+++ b/%s\n", path))
		b.WriteString("@@ -0,0 +1,40 @@\n")
		for j := 0; j < linesPerFile; j++ {
			b.WriteString(fmt.Sprintf("+\tresult := compute_%02d_%02d(input, factor) // step %d\n", i, j, j))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func TestHierarchicalSummarize_ShortCircuitsWhenFits(t *testing.T) {
	var calls int32
	withFakeLLM(t, func(_ *config.Config, _, _ string) (string, error) {
		atomic.AddInt32(&calls, 1)
		return "should not be called", nil
	})

	cfg := testConfig()
	diff := makeLargeDiff(1, 2)
	budget := tokenizer.CountTokens(diff, "gpt-4") + 100

	out, err := HierarchicalSummarize(cfg, diff, budget)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != diff {
		t.Errorf("expected diff returned unchanged when it already fits")
	}
	if c := atomic.LoadInt32(&calls); c != 0 {
		t.Errorf("expected 0 LLM calls for already-fitting input, got %d", c)
	}
}

func TestHierarchicalSummarize_ConvergesAndPreservesPaths(t *testing.T) {
	withFakeLLM(t, keepRatioCompressor(0.4))

	cfg := testConfig()
	n := 20
	diff := makeLargeDiff(n, 40)
	budget := 500

	inputTokens := tokenizer.CountTokens(diff, "gpt-4")
	if inputTokens <= budget {
		t.Fatalf("test setup invalid: input %d already under budget %d", inputTokens, budget)
	}

	out, err := HierarchicalSummarize(cfg, diff, budget)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	finalTokens := tokenizer.CountTokens(out, "gpt-4")
	maxAllowed := budget + budget*lastResortOverage/100
	if finalTokens > maxAllowed {
		t.Errorf("final tokens %d exceed budget %d (+%d%% overage = %d)", finalTokens, budget, lastResortOverage, maxAllowed)
	}

	for i := 0; i < n; i++ {
		path := fmt.Sprintf("module_%02d.go", i)
		if !strings.Contains(out, path) {
			t.Errorf("file path %q missing from summary output", path)
		}
	}
}

func TestHierarchicalSummarize_NearBudgetOverageSummarizesMinimalFiles(t *testing.T) {
	var calls int32
	withFakeLLM(t, func(_ *config.Config, _, user string) (string, error) {
		atomic.AddInt32(&calls, 1)
		if !strings.Contains(user, "module_") {
			t.Errorf("expected file diff in summarization prompt")
		}
		return "pkg/feature/module_summary.go: summarized large change", nil
	})

	cfg := testConfig()
	diff := makeLargeDiff(26, 40)
	inputTokens := tokenizer.CountTokens(diff, "gpt-4")
	budget := inputTokens - 500
	if budget <= 0 {
		t.Fatalf("test setup invalid: budget %d", budget)
	}

	out, err := HierarchicalSummarize(cfg, diff, budget)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if c := atomic.LoadInt32(&calls); c >= 26 {
		t.Fatalf("expected selective summarization, got %d LLM calls", c)
	}
	if c := atomic.LoadInt32(&calls); c == 0 {
		t.Fatalf("expected at least one LLM call for over-budget input")
	}
	if finalTokens := tokenizer.CountTokens(out, "gpt-4"); finalTokens > budget {
		t.Fatalf("final tokens %d exceed budget %d", finalTokens, budget)
	}
}

func TestReduceSummarize_ShrinksRoundOverRound(t *testing.T) {
	withFakeLLM(t, keepRatioCompressor(0.4))

	cfg := testConfig()
	// Build an already-mapped summary blob (sections separated by blank lines).
	var b strings.Builder
	for i := 0; i < 30; i++ {
		b.WriteString(fmt.Sprintf("pkg/feature/module_%02d.go: added compute_%02d, helper_%02d\n", i, i, i))
		for j := 0; j < 6; j++ {
			b.WriteString(fmt.Sprintf("  detail line %d describing behavior change number %d here\n", j, j))
		}
		b.WriteString("\n")
	}
	current := strings.TrimSpace(b.String())
	budget := 400

	inTokens := tokenizer.CountTokens(current, "gpt-4")
	reduced, newTokens, err := reduceSummarize(cfg, current, budget, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if newTokens >= inTokens {
		t.Errorf("reduce did not shrink: %d -> %d tokens", inTokens, newTokens)
	}
	if strings.TrimSpace(reduced) == "" {
		t.Errorf("reduce produced empty output")
	}
}

func TestHierarchicalSummarize_PartialChunkFailureStillCompletes(t *testing.T) {
	// Fake errors whenever the input references module_00, succeeds otherwise.
	compressor := keepRatioCompressor(0.4)
	withFakeLLM(t, func(cfg *config.Config, sys, user string) (string, error) {
		if strings.Contains(user, "module_00.go") {
			return "", fmt.Errorf("simulated provider failure")
		}
		return compressor(cfg, sys, user)
	})

	cfg := testConfig()
	n := 12
	diff := makeLargeDiff(n, 40)
	budget := 500

	out, err := HierarchicalSummarize(cfg, diff, budget)
	if err != nil {
		t.Fatalf("expected completion despite partial chunk failure, got error: %v", err)
	}

	finalTokens := tokenizer.CountTokens(out, "gpt-4")
	maxAllowed := budget + budget*lastResortOverage/100
	if finalTokens > maxAllowed {
		t.Errorf("final tokens %d exceed budget %d (+overage %d)", finalTokens, budget, maxAllowed)
	}
	// The failing file should still be represented (as a stub) rather than silently dropped.
	if !strings.Contains(out, "module_00.go") {
		t.Errorf("failing file module_00.go missing from output; it should survive as a stub")
	}
}
