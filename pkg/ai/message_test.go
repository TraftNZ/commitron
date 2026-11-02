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

func TestTrimBody_DropsWholeBullets(t *testing.T) {
	body := "- first bullet explaining a real change\n" +
		"- second bullet explaining another change\n" +
		"- third bullet that will not fit"

	got := trimBody(body, 80)

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

func TestTrimBody_ProseEndsOnSentence(t *testing.T) {
	body := "Compact diffs without calling the model. Each extra call costs a full prompt evaluation on local servers."

	got := trimBody(body, 60)

	if got != "Compact diffs without calling the model." {
		t.Errorf("expected a cut at the sentence boundary, got %q", got)
	}
}

func TestTrimBody_SingleSentenceEndsOnWord(t *testing.T) {
	got := trimBody("compact diffs without calling the model", 20)

	if got != "compact diffs" {
		t.Errorf("expected a cut at the word boundary, got %q", got)
	}
}

func TestFormatCommitMessage_ProseBodyIsWrappedNotBulleted(t *testing.T) {
	cfg := conventionalCfg()
	msg := CommitMessage{
		Type:    "refactor",
		Scope:   "ai",
		Subject: "compact diffs deterministically",
		Body: "Replace LLM summarization with deterministic compaction so large diffs fit the budget.\n" +
			"Each summarization call cost a full prompt evaluation on local servers, which dominated latency.",
	}

	got := FormatCommitMessage(msg, cfg)

	subject, body, found := strings.Cut(got, "\n\n")
	if !found || subject != "refactor(ai): compact diffs deterministically" {
		t.Fatalf("unexpected subject formatting:\n%s", got)
	}
	if strings.Contains(body, "\n\n") {
		t.Errorf("a broken paragraph should be rejoined:\n%s", body)
	}
	for _, line := range strings.Split(body, "\n") {
		if isListItem(line) {
			t.Errorf("prose was turned into a list item: %q", line)
		}
		if len(line) > bodyWrapWidth {
			t.Errorf("line exceeds %d chars: %q", bodyWrapWidth, line)
		}
	}
	if !strings.Contains(strings.Join(strings.Fields(body), " "), "evaluation on local servers, which dominated latency.") {
		t.Errorf("body text was lost:\n%s", body)
	}
}

func TestFormatBody_WrapsBulletsWithHangingIndent(t *testing.T) {
	first := "- Stage new source files automatically so a commit no longer leaves them behind by accident"
	second := "- Skip submodules with no changes"

	lines := strings.Split(formatBody(first+"\n"+second), "\n")

	if !strings.HasPrefix(lines[0], "- Stage") || !strings.HasPrefix(lines[1], "  ") {
		t.Errorf("expected a bullet with an indented continuation, got %q", lines)
	}
	if got := lines[len(lines)-1]; got != second {
		t.Errorf("expected the short bullet untouched, got %q", got)
	}
	for _, line := range lines {
		if len(line) > bodyWrapWidth {
			t.Errorf("line exceeds %d chars: %q", bodyWrapWidth, line)
		}
	}
}

func TestFormatBody_JoinsBulletBrokenAcrossLines(t *testing.T) {
	got := formatBody("- Skip clean\n  submodules\n- Stop on failure")

	if want := "- Skip clean submodules\n- Stop on failure"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormatCommitMessage_KeepsParagraphBreaks(t *testing.T) {
	cfg := conventionalCfg()
	msg := CommitMessage{Type: "fix", Subject: "bound retries", Body: "First paragraph.\n\n\nSecond paragraph."}

	if got, want := FormatCommitMessage(msg, cfg), "fix: bound retries\n\nFirst paragraph.\n\nSecond paragraph."; got != want {
		t.Errorf("got %q, want %q", got, want)
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

	if ceiling := maxResponseTokens + cfg.AI.ReasoningMaxTokens; got > ceiling {
		t.Errorf("response budget %d exceeds cap %d", got, ceiling)
	}
	if got >= cfg.AI.MaxTokens {
		t.Errorf("response budget %d inherited the context-sized max_tokens %d", got, cfg.AI.MaxTokens)
	}
	if got < 200 {
		t.Errorf("response budget %d is too small to hold a message", got)
	}
}

// A reasoning model spends its thinking against the same ceiling as the message, so the
// budget must carry headroom beyond what the message itself needs. Without it the model
// is cut off mid-thought having emitted nothing, which used to commit a bare "chore:".
func TestResponseTokenBudget_ReservesReasoningHeadroom(t *testing.T) {
	cfg := conventionalCfg()
	cfg.AI.MaxTokens = 32768
	cfg.Commit.MaxBodyLength = 8000

	withReasoning := responseTokenBudget(cfg)

	cfg.AI.ReasoningMaxTokens = 0
	withoutReasoning := responseTokenBudget(cfg)

	if withReasoning-withoutReasoning != config.DefaultReasoningMaxTokens {
		t.Errorf("headroom not reserved: %d with reasoning, %d without", withReasoning, withoutReasoning)
	}
}

// A response carrying no subject must be reported as a failure. parseTextCommitMessage
// always defaults a type, so a message with an empty subject used to be returned as a
// success and committed as a bare "chore:".
func TestParseCommitMessageJSON_RejectsSubjectlessResponses(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"whitespace", "   \n\t\n  "},
		{"unterminated reasoning block", "<think>\nthe diff deletes two files, so maybe refactor"},
		{"json without a subject", `{"type": "chore", "scope": "", "subject": ""}`},
		{"fence with nothing in it", "```\n```"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseCommitMessageJSON(tt.in)
			if err == nil {
				t.Errorf("expected an error, got message %+v", got)
			}
		})
	}
}

func TestParseCommitMessageJSON_KeepsValidResponses(t *testing.T) {
	got, err := ParseCommitMessageJSON("feat(ai): add reasoning headroom\n\n- widen the response budget")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Type != "feat" || got.Scope != "ai" || got.Subject != "add reasoning headroom" {
		t.Errorf("parsed wrongly: %+v", got)
	}
	if !strings.Contains(got.Body, "widen the response budget") {
		t.Errorf("body lost: %q", got.Body)
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
	for _, want := range []string{"feat", "fix", "imperative", "summarize", "plain English", "why", "instead of inventing"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt lost essential guidance %q", want)
		}
	}
}

func TestGenerateTextPrompt_SubjectOnlyOmitsBodyFormat(t *testing.T) {
	cfg := conventionalCfg()
	cfg.Commit.IncludeBody = false
	cfg.Context.IncludeRecentCommits = 0

	prompt := GenerateTextPrompt(cfg, nil, "")

	if strings.Contains(prompt, "summary point") {
		t.Errorf("a subject-only prompt should not ask for a body:\n%s", prompt)
	}
}

func TestFormatBody_WhitespaceOnly(t *testing.T) {
	if got := formatBody("  \n "); got != "" {
		t.Errorf("expected an empty body, got %q", got)
	}
}
