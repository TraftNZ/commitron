package config

import (
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// CommitConvention represents the convention to use for commit messages
type CommitConvention string

const (
	// NoConvention indicates no specific convention
	NoConvention CommitConvention = "none"
	// ConventionalCommits follows the conventional commits spec
	ConventionalCommits CommitConvention = "conventional"
	// CustomConvention follows a custom convention defined in config
	CustomConvention CommitConvention = "custom"
)

// AIProvider represents the AI service to use
type AIProvider string

const (
	// OpenAI (ChatGPT) provider
	OpenAI AIProvider = "openai"
	// Google (Gemini) provider
	Gemini AIProvider = "gemini"
	// Ollama (local) provider
	Ollama AIProvider = "ollama"
	// Anthropic (Claude) provider
	Claude AIProvider = "claude"
)

// Config represents the application configuration
type Config struct {
	// AI provider configuration
	AI struct {
		Provider       AIProvider `yaml:"provider"`
		APIKey         string     `yaml:"api_key"`
		Model          string     `yaml:"model"`
		OllamaHost     string     `yaml:"ollama_host,omitempty"`
		OpenAIEndpoint string     `yaml:"openai_endpoint,omitempty"` // Custom OpenAI API endpoint
		Temperature    float64    `yaml:"temperature"`
		SystemPrompt   string     `yaml:"system_prompt"`
		Debug          bool       `yaml:"debug,omitempty"`      // When true, prints debug info about AI requests
		MaxTokens      int        `yaml:"max_tokens,omitempty"` // Maximum tokens to generate in response
	} `yaml:"ai"`

	// Commit message configuration
	Commit struct {
		Convention     CommitConvention `yaml:"convention"`
		IncludeBody    bool             `yaml:"include_body"`
		MaxLength      int              `yaml:"max_length"`
		MaxBodyLength  int              `yaml:"max_body_length"` // Maximum length for the commit body
		CustomTemplate string           `yaml:"custom_template,omitempty"`
	} `yaml:"commit"`

	// Additional context to provide to the AI
	Context struct {
		IncludeFileNames     bool   `yaml:"include_file_names"`                 // Include file names in the context
		IncludeDiff          bool   `yaml:"include_diff"`                       // Include the diff in the context
		IncludeFileStats     bool   `yaml:"include_file_stats"`                 // Include stats about file changes (+/- lines)
		IncludeFileSummaries bool   `yaml:"include_file_summaries"`             // Include brief description of what each file does
		ShowFirstLinesOfFile int    `yaml:"show_first_lines_of_file,omitempty"` // Show first N lines of each file for better context
		IncludeRepoStructure bool   `yaml:"include_repo_structure,omitempty"`   // Include high-level repo structure
		MaxInputTokens       int    `yaml:"max_input_tokens,omitempty"`         // Maximum tokens for input context (replaces MaxContextLength)
		DiffStrategy         string `yaml:"diff_strategy,omitempty"`            // Strategy for handling large diffs: "auto", "summarize", "batch", "truncate"
		TokenizerModel       string `yaml:"tokenizer_model,omitempty"`          // Model to use for token counting (empty = use AI model)
		SummarizationEnabled bool   `yaml:"summarization_enabled,omitempty"`    // Enable smart diff summarization
	} `yaml:"context"`

	// User interface configuration
	UI struct {
		EnableTUI         bool `yaml:"enable_tui"`          // Enable TUI for better visualization
		ConfirmCommit     bool `yaml:"confirm_commit"`      // Ask for confirmation before committing
		DisplayFilesLimit int  `yaml:"display_files_limit"` // Maximum files to display in the UI (0 = no limit)
	} `yaml:"ui"`
}

// DefaultConfig returns the default configuration
func DefaultConfig() *Config {
	cfg := &Config{}

	// Default AI settings
	cfg.AI.Provider = OpenAI
	cfg.AI.Model = "gpt-3.5-turbo"
	cfg.AI.OpenAIEndpoint = "https://api.openai.com/v1/chat/completions"
	cfg.AI.Temperature = 0.7
	cfg.AI.SystemPrompt = ""
	cfg.AI.Debug = false
	cfg.AI.MaxTokens = 4000

	// Default commit settings
	cfg.Commit.Convention = NoConvention
	cfg.Commit.IncludeBody = true
	cfg.Commit.MaxLength = 120
	cfg.Commit.MaxBodyLength = 1000 // Default maximum body length

	// Default context settings
	cfg.Context.IncludeFileNames = true
	cfg.Context.IncludeDiff = true
	cfg.Context.IncludeFileStats = false
	cfg.Context.IncludeFileSummaries = false
	cfg.Context.ShowFirstLinesOfFile = 0
	cfg.Context.IncludeRepoStructure = false
	cfg.Context.MaxInputTokens = 100000          // 100K tokens (safe under most model limits)
	cfg.Context.DiffStrategy = "auto"            // Auto-select strategy based on size
	cfg.Context.TokenizerModel = ""              // Empty = use cfg.AI.Model
	cfg.Context.SummarizationEnabled = true

	// Default UI settings
	cfg.UI.EnableTUI = true
	cfg.UI.ConfirmCommit = true
	cfg.UI.DisplayFilesLimit = 20

	return cfg
}

// ParseConfig parses a configuration from YAML data
func ParseConfig(data []byte) (*Config, error) {
	cfg := DefaultConfig()

	// Parse YAML
	err := yaml.Unmarshal(data, cfg)
	if err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadConfig loads the configuration from ~/.commitronrc
func LoadConfig() (*Config, error) {
	// Get user's home directory
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return DefaultConfig(), err
	}

	configPath := filepath.Join(homeDir, ".commitronrc")
	return LoadConfigFromPath(configPath)
}

// LoadConfigFromPath loads configuration from a specified path
// If the file doesn't exist, returns default configuration
func LoadConfigFromPath(configPath string) (*Config, error) {
	cfg := DefaultConfig()

	// Check if config file exists
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		// No config file, just return defaults
		return cfg, nil
	}

	// Read config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return cfg, err
	}

	return ParseConfig(data)
}

// SaveExampleConfig saves an example configuration to the given path
func SaveExampleConfig(path string) error {
	cfg := DefaultConfig()

	// Add some example values
	cfg.AI.Provider = OpenAI
	cfg.AI.APIKey = "your-api-key-here"
	cfg.AI.Model = "gpt-3.5-turbo"
	cfg.AI.Temperature = 0.7 // Example temperature value
	cfg.AI.Debug = false     // Set to true to see AI prompts and responses
	cfg.AI.MaxTokens = 4000  // Maximum response tokens

	// Example of a custom system prompt (commented out by default)
	cfg.AI.SystemPrompt = "# Custom system prompt (uncomment to use)\n# You are an expert developer who writes clear, concise commit messages.\n# Always follow the conventional commits format and be specific."

	cfg.Commit.Convention = ConventionalCommits
	cfg.Commit.CustomTemplate = "{{type}}({{scope}}): {{subject}}"

	// Set example context values
	cfg.Context.IncludeFileStats = false
	cfg.Context.IncludeFileSummaries = false
	cfg.Context.ShowFirstLinesOfFile = 0
	cfg.Context.IncludeRepoStructure = false

	// Set example UI values
	cfg.UI.EnableTUI = true
	cfg.UI.ConfirmCommit = true
	cfg.UI.DisplayFilesLimit = 20

	// Marshal to YAML
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}

	// Add comments to the YAML
	yamlWithComments := `# Commitron configuration file
# This file configures the behavior of the commitron tool

` + string(data)

	// Write to file
	return os.WriteFile(path, []byte(yamlWithComments), 0644)
}
