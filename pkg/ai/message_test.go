package ai

import (
	"strings"
	"testing"

	"github.com/johnstilia/commitron/pkg/config"
)

func conventionalCfg() *config.Config {
	cfg := config.DefaultConfig()
	cfg.Commit.Convention = config.ConventionalCommits
	cfg.Commit.IncludeBody = true
	cfg.Commit.MaxLength = 72
	cfg.Commit.MaxBodyLength = 1000
	return cfg
}

func TestSanitizeResponse(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips inline reasoning block",
			in:   "<think>Let me look at the diff. It changes the parser.</think>\nfix(parser): handle empty input",
			want: "fix(parser): handle empty input",
		},
		{
			name: "strips unterminated reasoning block",
			in:   "fix(parser): handle empty input\n<think>now let me double check",
			want: "fix(parser): handle empty input",
		},
		{
			name: "strips code fences",
			in:   "```\nfeat: add retry logic\n\n- retry failed calls\n```",
			want: "feat: add retry logic\n\n- retry failed calls",
		},
		{
			name: "strips conversational preamble",
			in:   "Here is the commit message:\n\nfeat: add retry logic",
			want: "feat: add retry logic",
		},
		{
			name: "leaves a clean message untouched",
			in:   "fix(git): stage tracked files only\n\n- use git add -u",
			want: "fix(git): stage tracked files only\n\n- use git add -u",
		},
		{
			// A commit message describing this very code mentions the tags in prose.
			// Treating a mid-line mention as a block opener truncated the body there.
			name: "keeps a mid-line mention of a reasoning tag",
			in: "feat(ai): sanitize model output\n\n" +
				"- Add `sanitizeResponse` to strip reasoning blocks (`<thinking>`, etc.) before parsing\n" +
				"- Drop conversational preamble",
			want: "feat(ai): sanitize model output\n\n" +
				"- Add `sanitizeResponse` to strip reasoning blocks (`<thinking>`, etc.) before parsing\n" +
				"- Drop conversational preamble",
		},
		{
			name: "keeps prose naming both tags on one line",
			in:   "docs: explain markers\n\n- describe `<think>` and `</think>` handling",
			want: "docs: explain markers\n\n- describe `<think>` and `</think>` handling",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeResponse(tt.in); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestNormalizeConventionalCommit_PreservesContent(t *testing.T) {
	// A body mentioning "the changes" and "files:" used to be gutted by keyword
	// heuristics, collapsing real content into generic filler.
	body := "- Rework the changes queue so this commit is idempotent\n" +
		"- Update files: config.go and git.go to share one staging helper"

	got := normalizeConventionalCommit(CommitMessage{
		Type:    "Feature",
		Scope:   "AI",
		Subject: "Add retry logic.",
		Body:    body,
	})

	if got.Type != "feat" {
		t.Errorf("type: got %q, want feat", got.Type)
	}
	if got.Scope != "ai" {
		t.Errorf("scope: got %q, want ai", got.Scope)
	}
	if got.Subject != "add retry logic" {
		t.Errorf("subject: got %q, want %q", got.Subject, "add retry logic")
	}
	if got.Body != body {
		t.Errorf("body was modified:\ngot  %q\nwant %q", got.Body, body)
	}
}

func TestNormalizeConventionalCommit_KeepsIdentifierCasing(t *testing.T) {
	got := normalizeConventionalCommit(CommitMessage{
		Type:    "fix",
		Subject: "GetStagedFiles now excludes untracked paths",
	})

	if !strings.HasPrefix(got.Subject, "GetStagedFiles") {
		t.Errorf("identifier casing must survive, got %q", got.Subject)
	}
}

func TestNormalizeConventionalCommit_UnknownTypeFallsBack(t *testing.T) {
	got := normalizeConventionalCommit(CommitMessage{Type: "wibble", Subject: "do a thing"})
	if got.Type != "chore" {
		t.Errorf("unknown type should fall back to chore, got %q", got.Type)
	}
}

func TestFitSubject(t *testing.T) {
	cfg := conventionalCfg()
	cfg.Commit.MaxLength = 50

	msg := CommitMessage{
		Type:    "feat",
		Scope:   "ai",
		Subject: "add deterministic diff compaction and drop summarizer",
	}

	got := fitSubject(msg, cfg)
	full := len(msg.Type) + len(msg.Scope) + len(got) + 4

	if full > cfg.Commit.MaxLength {
		t.Errorf("subject still too long: %d > %d (%q)", full, cfg.Commit.MaxLength, got)
	}
	if strings.HasSuffix(got, "...") {
		t.Errorf("should trim at a word boundary, not append an ellipsis: %q", got)
	}
	if strings.HasSuffix(got, " ") {
		t.Errorf("trailing whitespace left in %q", got)
	}
	// It must cut at a word boundary, never mid-word.
	if !strings.Contains(msg.Subject, got) {
		t.Errorf("result %q is not a prefix-slice of the original", got)
	}
}

func TestFitSubject_ShortSubjectUntouched(t *testing.T) {
	cfg := conventionalCfg()
	msg := CommitMessage{Type: "fix", Subject: "handle empty diff"}

	if got := fitSubject(msg, cfg); got != msg.Subject {
		t.Errorf("short subject should be untouched, got %q", got)
	}
}

func TestTrimBodyToBullets(t *testing.T) {
	body := "- first bullet explaining a real change\n" +
		"- second bullet explaining another change\n" +
		"- third bullet that will not fit"

	got := trimBodyToBullets(body, 80)

	if len(got) > 80 {
		t.Errorf("body still over limit: %d chars", len(got))
	}
	if strings.Contains(got, "third bullet") {
		t.Error("expected the trailing bullet to be dropped")
	}
	if !strings.Contains(got, "first bullet explaining a real change") {
		t.Error("expected whole leading bullets to survive intact")
	}
	// Every surviving line must be a complete bullet.
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "- ") {
			t.Errorf("line was cut mid-bullet: %q", line)
		}
	}
}

func TestFormatCommitMessage_BulletBodyNotDoubled(t *testing.T) {
	cfg := conventionalCfg()
	msg := CommitMessage{
		Type:    "feat",
		Scope:   "ai",
		Subject: "compact diffs deterministically",
		Body:    "- drop LLM summarization round-trips\n- bound diff tokens",
	}

	got := FormatCommitMessage(msg, cfg)

	if !strings.HasPrefix(got, "feat(ai): compact diffs deterministically\n\n") {
		t.Errorf("unexpected subject formatting:\n%s", got)
	}
	if strings.Contains(got, "- - ") {
		t.Errorf("bullets were double-prefixed:\n%s", got)
	}
}

func TestGetRecentCommitSubjects_Disabled(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.Context.IncludeRecentCommits = 0

	if got := GetRecentCommitSubjects(cfg); got != "" {
		t.Errorf("expected no history when disabled, got %q", got)
	}
}

func TestResponseTokenBudget_IsBounded(t *testing.T) {
	cfg := conventionalCfg()
	cfg.AI.MaxTokens = 32768 // a context-sized budget must not be inherited
	cfg.Commit.MaxBodyLength = 8000

	got := responseTokenBudget(cfg)

	if got > maxResponseTokens {
		t.Errorf("response budget %d exceeds cap %d", got, maxResponseTokens)
	}
	if got < 200 {
		t.Errorf("response budget %d is too small to hold a message", got)
	}
}

func TestResponseTokenBudget_RespectsSmallerUserSetting(t *testing.T) {
	cfg := conventionalCfg()
	cfg.AI.MaxTokens = 120

	if got := responseTokenBudget(cfg); got != 120 {
		t.Errorf("an explicitly smaller max_tokens should win, got %d", got)
	}
}

func TestGenerateTextPrompt_IsCompact(t *testing.T) {
	cfg := conventionalCfg()
	cfg.Context.IncludeRecentCommits = 0
	cfg.Context.IncludeFileNames = false

	prompt := GenerateTextPrompt(cfg, nil, "")

	// The old prompt restated the same rules six ways and ran past 1,400 tokens.
	if n := len(prompt); n > 2000 {
		t.Errorf("instruction block grew back to %d chars:\n%s", n, prompt)
	}
	if strings.Count(prompt, "MUST NOT exceed") > 1 {
		t.Error("length rule is stated more than once")
	}
	for _, want := range []string{"feat", "fix", "imperative", "bullet"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lost essential guidance %q", want)
		}
	}
}
