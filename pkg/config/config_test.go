package config

import "testing"

// Removing a config key must not break existing ~/.commitronrc files. yaml.Unmarshal
// ignores unknown keys, so a config still carrying the retired diff_strategy and
// summarization_enabled settings has to load with defaults intact.
func TestParseConfig_IgnoresRetiredKeys(t *testing.T) {
	data := []byte(`
ai:
  provider: openai
  model: qwen36
context:
  include_diff: true
  max_input_tokens: 100000
  diff_strategy: batch
  summarization_enabled: false
`)

	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("legacy config failed to parse: %v", err)
	}
	if cfg.AI.Model != "qwen36" {
		t.Errorf("model: got %q, want qwen36", cfg.AI.Model)
	}
	if cfg.Context.MaxInputTokens != 100000 {
		t.Errorf("max_input_tokens: got %d", cfg.Context.MaxInputTokens)
	}
	if cfg.Context.IncludeRecentCommits != 10 {
		t.Errorf("include_recent_commits should keep its default, got %d", cfg.Context.IncludeRecentCommits)
	}
}

func TestParseConfig_ReadsDiffBudgetKeys(t *testing.T) {
	data := []byte(`
context:
  max_diff_tokens: 6000
  include_recent_commits: 0
`)

	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Context.MaxDiffTokens != 6000 {
		t.Errorf("max_diff_tokens: got %d, want 6000", cfg.Context.MaxDiffTokens)
	}
	if cfg.Context.IncludeRecentCommits != 0 {
		t.Errorf("include_recent_commits: got %d, want 0", cfg.Context.IncludeRecentCommits)
	}
}

func TestDefaultConfig_DiffBudgetDefaults(t *testing.T) {
	cfg := DefaultConfig()

	// 0 means "use the package default" rather than "send no diff".
	if cfg.Context.MaxDiffTokens != 0 {
		t.Errorf("max_diff_tokens default: got %d, want 0", cfg.Context.MaxDiffTokens)
	}
	if cfg.Context.IncludeRecentCommits != 10 {
		t.Errorf("include_recent_commits default: got %d, want 10", cfg.Context.IncludeRecentCommits)
	}
}
