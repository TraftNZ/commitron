package ai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/johnstilia/commitron/pkg/config"
)

const defaultRequestTimeoutSeconds = 300

// httpClientForConfig returns a bounded client for provider calls. The default is
// intentionally higher than local/relay model cold paths observed on large diffs.
func httpClientForConfig(cfg *config.Config) *http.Client {
	timeoutSeconds := cfg.AI.RequestTimeoutSeconds
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultRequestTimeoutSeconds
	}

	return &http.Client{
		Timeout: time.Duration(timeoutSeconds) * time.Second,
	}
}

// CallLLM calls the configured AI provider with the given system and user prompts.
// It handles provider-specific prompt formatting uniformly for both summarization and final commit generation.
// For the final commit message generation, streaming is enabled to show output as it arrives.
func CallLLM(cfg *config.Config, systemPrompt, userPrompt string) (string, error) {
	// Add the universal length requirement prefix that used to be duplicated per-provider
	lengthPrefix := fmt.Sprintf("CRITICAL INSTRUCTION: Your output must be concise and fit within token limits. " +
		"Preserve all critical information including file paths, function names, and BREAKING CHANGE markers. " +
		"If summarizing, keep the summary focused on what actually changed.")

	systemPrompt = lengthPrefix + "\n\n" + systemPrompt

	switch cfg.AI.Provider {
	case config.OpenAI:
		return callOpenAI(cfg, systemPrompt, userPrompt, true)
	case config.Gemini:
		return callGemini(cfg, systemPrompt, userPrompt)
	case config.Ollama:
		return callOllama(cfg, systemPrompt, userPrompt, true)
	case config.Claude:
		return callClaude(cfg, systemPrompt, userPrompt)
	default:
		return "", fmt.Errorf("unsupported AI provider: %s", cfg.AI.Provider)
	}
}

// callOpenAI makes a request to OpenAI-compatible API with separate system and user messages
// Supports streaming output to show response as it's generated
func callOpenAI(cfg *config.Config, systemPrompt, userPrompt string, stream bool) (string, error) {
	type Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	type Request struct {
		Model       string    `json:"model"`
		Messages    []Message `json:"messages"`
		MaxTokens   int       `json:"max_tokens,omitempty"`
		Temperature float64   `json:"temperature,omitempty"`
		Stream      bool      `json:"stream,omitempty"`
	}

	type ChunkDelta struct {
		Content          string `json:"content"`
		ReasoningContent string `json:"reasoning_content"`
	}

	type ChunkResponse struct {
		Choices []struct {
			Delta ChunkDelta `json:"delta"`
		} `json:"choices"`
	}

	type ErrorResponse struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code"`
	}

	messages := []Message{
		{
			Role:    "system",
			Content: systemPrompt,
		},
		{
			Role:    "user",
			Content: userPrompt,
		},
	}

	reqBody := Request{
		Model:       cfg.AI.Model,
		Messages:    messages,
		MaxTokens:   cfg.AI.MaxTokens,
		Temperature: cfg.AI.Temperature,
		Stream:      stream,
	}

	debugPrint(cfg, "OPENAI REQUEST", reqBody)

	reqData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	endpoint := cfg.AI.OpenAIEndpoint
	if endpoint == "" {
		endpoint = "https://api.openai.com/v1/chat/completions"
	}

	req, err := http.NewRequest("POST", endpoint, bytes.NewBuffer(reqData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.AI.APIKey)

	resp, err := httpClientForConfig(cfg).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	// Check for non-200 status before streaming
	if resp.StatusCode != http.StatusOK {
		respData, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("OpenAI API error (status %d): %s", resp.StatusCode, string(respData))
	}

	// Handle streaming response
	if stream {
		var fullContent strings.Builder
		scanner := bufio.NewScanner(resp.Body)

		fmt.Printf("\033[1;36m💬 Generating:\033[0m\n")
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				break
			}

			var chunk ChunkResponse
			if err := json.Unmarshal([]byte(data), &chunk); err != nil {
				continue // skip malformed chunks
			}

			if len(chunk.Choices) > 0 {
				delta := chunk.Choices[0].Delta
				if delta.ReasoningContent != "" {
					// Print reasoning in dim gray to distinguish from final content
					fmt.Printf("\033[38;5;244m%s\033[0m", delta.ReasoningContent)
					os.Stdout.Sync()
				}
				if delta.Content != "" {
					fullContent.WriteString(delta.Content)
					// Print the content in default color
					fmt.Print(delta.Content)
					os.Stdout.Sync()
				}
			}
		}

		fmt.Println() // Final newline
		content := strings.TrimSpace(fullContent.String())
		return content, scanner.Err()
	}

	// Non-streaming response
	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	debugPrint(cfg, "OPENAI RAW RESPONSE", string(respData))

	type FullResponse struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Error json.RawMessage `json:"error,omitempty"`
	}

	var response FullResponse
	err = json.Unmarshal(respData, &response)
	if err != nil {
		return "", fmt.Errorf("error parsing API response: %w (response was: %s)", err, string(respData))
	}

	if len(response.Error) > 0 {
		var errorMessage string
		var errResp ErrorResponse
		if err := json.Unmarshal(response.Error, &errResp); err == nil && errResp.Message != "" {
			errorMessage = errResp.Message
		} else {
			var errStr string
			if err := json.Unmarshal(response.Error, &errStr); err == nil && errStr != "" {
				errorMessage = errStr
			} else {
				errorMessage = string(response.Error)
			}
		}

		if strings.Contains(errorMessage, "maximum context length") || strings.Contains(errorMessage, "context_length_exceeded") {
			return "", fmt.Errorf("OpenAI API error: %s\n\nChangeset too large even after hierarchical summarization. Consider:\n"+
				"  1. Split into smaller commits\n"+
				"  2. Reduce max_input_tokens in your config\n"+
				"  3. Disable include_diff temporarily", errorMessage)
		}

		return "", fmt.Errorf("OpenAI API error: %s", errorMessage)
	}

	if len(response.Choices) == 0 {
		return "", fmt.Errorf("no response from OpenAI API")
	}

	content := strings.TrimSpace(response.Choices[0].Message.Content)
	return content, nil
}

// callGemini makes a request to Google Gemini API with concatenated prompts
func callGemini(cfg *config.Config, systemPrompt, userPrompt string) (string, error) {
	// Gemini doesn't have separate system message in v1beta API - concatenate
	fullPrompt := systemPrompt + "\n\n" + userPrompt

	type Request struct {
		Contents []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		} `json:"contents"`
	}

	type Response struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	reqBody := Request{
		Contents: []struct {
			Parts []struct {
				Text string `json:"text"`
			} `json:"parts"`
		}{
			{
				Parts: []struct {
					Text string `json:"text"`
				}{
					{
						Text: fullPrompt,
					},
				},
			},
		},
	}

	debugPrint(cfg, "GEMINI REQUEST", reqBody)

	reqData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	apiURL := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent?key=%s", cfg.AI.Model, cfg.AI.APIKey)
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(reqData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClientForConfig(cfg).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	debugPrint(cfg, "GEMINI RAW RESPONSE", string(respData))

	var response Response
	err = json.Unmarshal(respData, &response)
	if err != nil {
		return "", err
	}

	if response.Error.Message != "" {
		return "", fmt.Errorf("Gemini API error: %s", response.Error.Message)
	}

	if len(response.Candidates) == 0 || len(response.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no response from Gemini API")
	}

	content := strings.TrimSpace(response.Candidates[0].Content.Parts[0].Text)
	return content, nil
}

// callOllama makes a request to local Ollama with concatenated prompts
// Supports streaming output to show response as it's generated
func callOllama(cfg *config.Config, systemPrompt, userPrompt string, stream bool) (string, error) {
	// Ollama generate endpoint takes a single prompt - concatenate
	fullPrompt := systemPrompt + "\n\n" + userPrompt

	type Request struct {
		Model       string  `json:"model"`
		Prompt      string  `json:"prompt"`
		Stream      bool    `json:"stream"`
		Temperature float64 `json:"temperature,omitempty"`
		MaxTokens   int     `json:"max_tokens,omitempty"`
	}

	type ChunkResponse struct {
		Response string `json:"response"`
		Done     bool   `json:"done"`
	}

	ollamaHost := cfg.AI.OllamaHost
	if ollamaHost == "" {
		ollamaHost = "http://localhost:11434"
	}

	reqBody := Request{
		Model:       cfg.AI.Model,
		Prompt:      fullPrompt,
		Stream:      stream,
		Temperature: cfg.AI.Temperature,
		MaxTokens:   cfg.AI.MaxTokens,
	}

	debugPrint(cfg, "OLLAMA REQUEST", reqBody)
	debugPrint(cfg, "OLLAMA HOST", ollamaHost)

	reqData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", ollamaHost+"/api/generate", bytes.NewBuffer(reqData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClientForConfig(cfg).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("Ollama API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	// Handle streaming response
	if stream {
		var fullContent strings.Builder
		scanner := bufio.NewScanner(resp.Body)

		fmt.Printf("\033[1;36m💬 Generating:\033[0m\n")
		for scanner.Scan() {
			line := scanner.Text()
			var chunk ChunkResponse
			if err := json.Unmarshal([]byte(line), &chunk); err != nil {
				continue // skip malformed chunks
			}

			if chunk.Response != "" {
				fullContent.WriteString(chunk.Response)
				// Print the chunk as we get it
				fmt.Print(chunk.Response)
				os.Stdout.Sync()
			}

			if chunk.Done {
				break
			}
		}

		fmt.Println() // Final newline
		content := strings.TrimSpace(fullContent.String())
		return content, scanner.Err()
	}

	// Non-streaming response
	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	debugPrint(cfg, "OLLAMA RAW RESPONSE", string(respData))

	type FullResponse struct {
		Model    string `json:"model"`
		Response string `json:"response"`
	}

	var response FullResponse
	err = json.Unmarshal(respData, &response)
	if err != nil {
		return "", fmt.Errorf("error parsing Ollama response: %w (response was: %s)", err, string(respData))
	}

	content := strings.TrimSpace(response.Response)
	return content, nil
}

// callClaude makes a request to Anthropic Claude API
func callClaude(cfg *config.Config, systemPrompt, userPrompt string) (string, error) {
	// Claude messages API uses separate system parameter
	// User prompt goes in the user message

	type Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}

	type Request struct {
		Model     string    `json:"model"`
		System    string    `json:"system"`
		Messages  []Message `json:"messages"`
		MaxTokens int       `json:"max_tokens"`
	}

	type Response struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	messages := []Message{
		{
			Role:    "user",
			Content: userPrompt,
		},
	}

	reqBody := Request{
		Model:     cfg.AI.Model,
		System:    systemPrompt,
		Messages:  messages,
		MaxTokens: cfg.AI.MaxTokens,
	}

	debugPrint(cfg, "CLAUDE REQUEST", reqBody)

	reqData, err := json.Marshal(reqBody)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(reqData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", cfg.AI.APIKey)
	req.Header.Set("Anthropic-Version", "2023-06-01")

	resp, err := httpClientForConfig(cfg).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	respData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	debugPrint(cfg, "CLAUDE RAW RESPONSE", string(respData))

	var response Response
	err = json.Unmarshal(respData, &response)
	if err != nil {
		return "", fmt.Errorf("error parsing Claude response: %w (response: %s)", err, string(respData))
	}

	if response.Error.Message != "" {
		return "", fmt.Errorf("Claude API error: %s", response.Error.Message)
	}

	if len(response.Content) == 0 || response.Content[0].Text == "" {
		return "", fmt.Errorf("no text content in Claude response")
	}

	content := strings.TrimSpace(response.Content[0].Text)
	return content, nil
}

// summarizeCall performs a single non-streaming LLM call for summarization steps.
// It is a package-level var so tests can inject a fake LLM without a live provider.
var summarizeCall = func(cfg *config.Config, systemPrompt, userPrompt string) (string, error) {
	// For non-streaming summarization, call directly with streaming disabled
	switch cfg.AI.Provider {
	case config.OpenAI:
		return callOpenAI(cfg, systemPrompt, userPrompt, false)
	case config.Ollama:
		return callOllama(cfg, systemPrompt, userPrompt, false)
	default:
		return CallLLM(cfg, systemPrompt, userPrompt)
	}
}

// callLLMWithRetry calls summarizeCall with retries and exponential backoff on failure.
// Used for summarization steps where individual failures can be retried.
// Summarization never uses streaming since we just need the full summary to continue.
func callLLMWithRetry(cfg *config.Config, systemPrompt, userPrompt string, maxRetries int) (string, error) {
	var lastErr error
	for i := 0; i <= maxRetries; i++ {
		result, err := summarizeCall(cfg, systemPrompt, userPrompt)

		if err == nil {
			return result, nil
		}
		lastErr = err
		if i < maxRetries {
			backoff := time.Duration(200*(1<<i)) * time.Millisecond
			time.Sleep(backoff)
			debugPrint(cfg, "LLM CALL RETRY", fmt.Sprintf("Attempt %d failed: %v, retrying in %v", i+1, err, backoff))
		}
	}
	return "", fmt.Errorf("LLM call failed after %d retries: %w", maxRetries, lastErr)
}
