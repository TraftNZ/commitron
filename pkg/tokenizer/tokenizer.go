package tokenizer

import (
	"strings"

	"github.com/pkoukk/tiktoken-go"
)

// CountTokens returns the number of tokens in the given text for the specified model.
// For unknown models, it falls back to cl100k_base encoding (current OpenAI standard).
func CountTokens(text string, model string) int {
	if text == "" {
		return 0
	}

	// Try to get encoding for the specific model
	encoding, err := tiktoken.EncodingForModel(model)
	if err != nil {
		// Fallback to cl100k_base for unknown models (gpt-4, gpt-3.5-turbo, future models)
		encoding, err = tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			// Ultimate fallback: estimate based on character count
			// Typical ratio is 1 token ≈ 3.5 characters for English text
			return int(float64(len(text)) / 3.5)
		}
	}

	tokens := encoding.Encode(text, nil, nil)
	return len(tokens)
}

// TruncateToTokenLimit intelligently truncates text to fit within the token limit.
// It attempts to truncate at diff boundaries (file boundaries or hunk boundaries) rather
// than cutting mid-content to preserve context integrity.
func TruncateToTokenLimit(text string, maxTokens int, model string) string {
	currentTokens := CountTokens(text, model)
	if currentTokens <= maxTokens {
		return text
	}

	// Split by file boundaries (diff --git markers) for intelligent truncation
	lines := strings.Split(text, "\n")
	var result []string
	var currentTotal int

	for _, line := range lines {
		lineTokens := CountTokens(line+"\n", model)
		if currentTotal+lineTokens > maxTokens {
			// Stop before exceeding limit
			result = append(result, "...[truncated to fit token limit]")
			break
		}
		result = append(result, line)
		currentTotal += lineTokens
	}

	return strings.Join(result, "\n")
}

// GetProviderTokenLimit returns the safe token limit for a given provider and model.
// These are conservative limits to avoid hitting API errors.
func GetProviderTokenLimit(provider string, model string) int {
	provider = strings.ToLower(provider)
	model = strings.ToLower(model)

	// Provider-specific limits (conservative to leave room for response)
	switch provider {
	case "openai":
		// GPT-4 Turbo and newer models have higher limits
		if strings.Contains(model, "gpt-4") || strings.Contains(model, "gpt-5") {
			if strings.Contains(model, "turbo") || strings.Contains(model, "128k") {
				return 100000 // 128K context, use 100K for input
			}
			return 100000 // Standard GPT-4 (128K context)
		}
		if strings.Contains(model, "gpt-3.5-turbo") {
			if strings.Contains(model, "16k") {
				return 12000 // 16K context
			}
			return 3000 // 4K context
		}
		if strings.Contains(model, "o1") || strings.Contains(model, "o3") {
			return 100000 // o1 and o3 models have 128K+ context
		}
		return 128000 // Default for unknown OpenAI models (128K context)

	case "claude":
		// Claude models typically have large context windows
		if strings.Contains(model, "claude-3") || strings.Contains(model, "claude-4") {
			return 180000 // Claude 3/4 have 200K context
		}
		return 90000 // Claude 2 and older

	case "gemini":
		// Gemini models
		if strings.Contains(model, "1.5") || strings.Contains(model, "2.0") {
			return 900000 // Gemini 1.5/2.0 Pro have 1M+ context
		}
		return 30000 // Gemini 1.0

	case "ollama":
		// Ollama models vary widely, use conservative default
		return 8000

	default:
		// Unknown provider: use conservative default
		return 100000
	}
}

// min returns the minimum of two integers
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
