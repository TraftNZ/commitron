# Commitron

AI-powered CLI tool that automatically generates intelligent, context-aware commit messages.

## Table of Contents

- [Features](#features)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Configuration](#configuration)
- [Usage Examples](#usage-examples)
- [Advanced Features](#advanced-features)
- [Troubleshooting](#troubleshooting)
- [License](#license)

## Features

- 🤖 **AI-Powered Commit Messages**: Generates meaningful, structured commit messages
- 🎯 **Token Optimization**: Handles large changesets (200K+ tokens) with smart summarization
- 📝 **Narrative Summaries**: Creates concise paragraph summaries explaining what changed and why
- 🗑️ **Complete Change Tracking**: Mentions both additions and deletions
- 🚀 **Auto-Staging**: Automatically stages tracked modified files (no manual `git add` needed)
- 🔧 **Custom Endpoints**: Works with OpenAI-compatible APIs (LocalAI, vLLM, etc.)
- 🧩 **Multiple AI Providers**: OpenAI, Claude, Gemini, Ollama (local)
- 📋 **Commit Conventions**: Conventional Commits, plain text, or custom templates
- ⚙️ **Fully Configurable**: Extensive YAML configuration
- 🎨 **Clean UI**: Colored output, progress indicators, file icons

## Installation

### Using Go Install

```bash
go install github.com/zhupengjia/commitron/cmd/commitron@latest
```

### Manual Installation

```bash
# Clone the repository
git clone https://github.com/zhupengjia/commitron.git
cd commitron

# Build using Make
make build

# Add to your PATH
cp bin/commitron /usr/local/bin/
```

### Dependencies

- Go 1.21+
- Dependencies managed automatically via Go modules

## Quick Start

1. **Initialize configuration:**
```bash
commitron init
```

2. **Edit `~/.commitronrc`** with your API key:
```yaml
ai:
  provider: openai
  api_key: sk-your-api-key-here
  model: gpt-4o
```

3. **Generate and commit:**
```bash
# Make some changes to your code
commitron
```

That's it! Commitron will auto-stage modified files and create a commit with an AI-generated message.

## Configuration

### Basic Configuration

Create `~/.commitronrc`:

```yaml
# AI provider configuration
ai:
  provider: openai              # openai, claude, gemini, ollama
  api_key: your-api-key-here   # Not needed for ollama
  model: gpt-4o                 # Model name for your provider
  temperature: 0.7
  max_tokens: 1000
  request_timeout_seconds: 300  # Increase for slow local/proxy models

# Commit message settings
commit:
  convention: conventional      # conventional, none, custom
  include_body: true           # Generate summary paragraph
  max_length: 120              # Subject line limit
  max_body_length: 1000        # Body limit

# Context settings
context:
  include_file_names: true
  include_diff: true
  max_input_tokens: 100000     # Token limit (100K for OpenAI)
  diff_strategy: auto          # auto, summarize, batch, truncate
  summarization_enabled: true

# UI settings
ui:
  enable_tui: true
  confirm_commit: false         # Auto-commit without confirmation
```

### Provider-Specific Settings

**OpenAI:**
```yaml
ai:
  provider: openai
  api_key: sk-your-key
  model: gpt-4o
```

**Claude:**
```yaml
ai:
  provider: claude
  api_key: sk-ant-your-key
  model: claude-3-5-sonnet-20241022
```

**Gemini:**
```yaml
ai:
  provider: gemini
  api_key: your-gemini-key
  model: gemini-2.0-flash-exp
```

**Ollama (Local):**
```yaml
ai:
  provider: ollama
  model: qwen2.5:latest
  ollama_host: http://localhost:11434
  # No API key needed
```

### Get API Keys

- **OpenAI**: https://platform.openai.com/api-keys
- **Claude**: https://console.anthropic.com/keys
- **Gemini**: https://aistudio.google.com/app/apikey
- **Ollama**: Run locally (no API key needed)

## Usage Examples

### Basic Usage

```bash
# Generate and commit (auto-stages tracked files)
commitron

# Preview message without committing
commitron --dry-run

# Use custom config file
commitron --config /path/to/config.yaml

# Show version
commitron version
```

### Example Output

```bash
$ commitron
⚠️  No staged files found. Automatically staging all modified files...
✓ Staged 4 files

🤖 Analyzing changes...

💬 Generated Commit Message
────────────────────────
feat(api): add rate limiting and request validation

Implemented token bucket rate limiter to prevent API abuse and added
comprehensive request validation middleware. Enhanced error handling
to return detailed validation errors with field-level feedback.
────────────────────────

💾 Creating commit... ✓ complete
```

### Auto-Staging Behavior

- **Tracked files only**: Only stages files already tracked by Git
- **Ignores untracked**: Never stages new files automatically
- **Smart detection**: Uses existing staged files if present
- **No manual staging**: No need to run `git add` before committing

### Build Commands

```bash
make build          # Build for current platform
make build-all      # Build for all platforms (Linux, macOS, Windows)
make test           # Run tests
make clean          # Clean build artifacts
```

## Advanced Features

### Custom OpenAI Endpoints

Use with any OpenAI-compatible API:

**LocalAI:**
```yaml
ai:
  provider: openai
  openai_endpoint: http://localhost:1234/v1/chat/completions
  model: gpt-4o
```

**vLLM:**
```yaml
ai:
  provider: openai
  openai_endpoint: http://localhost:8000/v1/chat/completions
  model: meta-llama/Llama-3.1-70B
```

**Corporate Proxy:**
```yaml
ai:
  provider: openai
  openai_endpoint: https://openai-proxy.company.com/v1/chat/completions
  api_key: your-corporate-key
```

### Token Optimization

Commitron automatically handles large changesets:

1. **Token Counting**: Accurately counts tokens for the AI model
2. **Smart Summarization**: Prioritizes important files (core logic > tests > docs)
3. **Batch Processing**: Handles extreme cases (>150K tokens)

**File Priority Scoring:**

| File Type | Priority | Treatment |
|-----------|----------|-----------|
| `pkg/ai/`, `pkg/git/` | High | Full diff context |
| `cmd/`, `pkg/` | Medium | Full or summarized |
| `*_test.go`, `*.md` | Low | Summarized only |

**Enable debug mode to see optimization:**
```yaml
ai:
  debug: true
```

### Diff Processing Strategies

Configure how large diffs are handled:

```yaml
context:
  diff_strategy: auto  # Options: auto, summarize, batch, truncate
```

- **auto**: Automatically selects strategy based on size (recommended)
- **summarize**: Priority-based summarization preserving key changes
- **batch**: Processes very large diffs in batches (200K+ tokens)
- **truncate**: Simple truncation at token boundary

### Token Limits by Provider

The system uses safe limits automatically:

- **OpenAI**: 100,000 tokens (safe under 128K limit)
- **Claude**: 180,000 tokens (safe under 200K limit)
- **Gemini**: 900,000 tokens (safe under 1M limit)
- **Ollama**: 8,000 tokens (conservative default)

Override if needed:
```yaml
context:
  max_input_tokens: 50000  # Use lower limit
```

## Troubleshooting

### Token Limit Errors

If you get "maximum context length exceeded" errors:

1. **Split commits**: Break changes into smaller logical chunks
2. **Use batch strategy**:
   ```yaml
   context:
     diff_strategy: batch
   ```
3. **Reduce token limit**:
   ```yaml
   context:
     max_input_tokens: 50000
   ```
4. **Temporary workaround** (generates generic message):
   ```yaml
   context:
     include_diff: false
   ```

### No Staged Files Warning

If you see "No staged files found" but have changes:

- Commitron only auto-stages **tracked** files
- New/untracked files must be manually added: `git add <file>`
- Check status: `git status`

### API Errors

**OpenAI rate limits:**
- Wait a moment and retry
- Use a different model tier

**Invalid API key:**
- Verify key in `~/.commitronrc`
- Check key has proper permissions

**Custom endpoint not working:**
- Verify endpoint URL is correct
- Ensure endpoint is OpenAI-compatible
- Check network connectivity

**Request timeout exceeded:**
- Increase `ai.request_timeout_seconds` in `~/.commitronrc`
- Reduce `context.max_input_tokens` for very large diffs
- Reduce `ai.max_tokens` if the endpoint spends too long generating

### Debug Mode

Enable detailed logging:

```yaml
ai:
  debug: true
```

Shows:
- Token counts before/after optimization
- Strategy selection reasoning
- File prioritization scores
- Full API requests/responses

## License

Distributed under the GPLv3 License. See [LICENSE.txt](LICENSE.txt) for more information.

## Acknowledgments

- Original project by [John Stilia](https://github.com/stiliajohny/commitron)
- Uses [tiktoken-go](https://github.com/pkoukk/tiktoken-go) for token counting

---

**Project**: https://github.com/zhupengjia/commitron

For more configuration options, see [example.commitronrc](example.commitronrc)
