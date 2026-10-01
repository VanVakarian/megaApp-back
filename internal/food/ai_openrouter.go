package food

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
)

const openRouterBaseURL = "https://openrouter.ai/api/v1"

type ProductGenerator interface {
	GenerateProduct(ctx context.Context, description string) (ProductPreviewData, error)
}

type OpenRouterProductGeneratorConfig struct {
	APIKey  string
	Model   string
	Timeout time.Duration
	Logger  *slog.Logger
}

type OpenRouterProductGenerator struct {
	client *openai.Client
	model  string
	logger *slog.Logger
}

type aiChatCompletionRequest struct {
	Model    string          `json:"model"`
	Messages []aiChatMessage `json:"messages"`
}

type aiChatMessage struct {
	Role    string              `json:"role"`
	Content []aiChatContentPart `json:"content"`
}

type aiChatContentPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type aiChatCompletionResponse struct {
	Choices []aiChatChoice `json:"choices"`
}

type aiChatChoice struct {
	Message aiChatResponseMessage `json:"message"`
}

type aiChatResponseMessage struct {
	Content string `json:"content"`
}

type aiProductResponse struct {
	GeneralizedName string   `json:"generalizedName"`
	Name            string   `json:"name"`
	Kcals           *float64 `json:"kcals"`
	Protein         *float64 `json:"protein"`
	Fat             *float64 `json:"fat"`
	Carbs           *float64 `json:"carbs"`
	Fiber           *float64 `json:"fiber"`
	Description     string   `json:"description"`
}

func NewOpenRouterProductGenerator(cfg OpenRouterProductGeneratorConfig) (*OpenRouterProductGenerator, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, errors.New("openrouter api key is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("openrouter model is required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 60 * time.Second
	}

	httpClient := &http.Client{Timeout: cfg.Timeout}
	client := openai.NewClient(
		option.WithAPIKey(cfg.APIKey),
		option.WithBaseURL(openRouterBaseURL),
		option.WithHTTPClient(httpClient),
		option.WithHeader("X-Title", "megaapp-back"),
	)

	return &OpenRouterProductGenerator{client: &client, model: cfg.Model, logger: cfg.Logger}, nil
}

func (g *OpenRouterProductGenerator) GenerateProduct(ctx context.Context, description string) (ProductPreviewData, error) {
	return g.generate(ctx, productGenerationSystemPrompt, fmt.Sprintf(productGenerationUserPrompt, strings.TrimSpace(description)))
}

func (g *OpenRouterProductGenerator) generate(ctx context.Context, systemPrompt string, userPrompt string) (ProductPreviewData, error) {
	if strings.TrimSpace(userPrompt) == "" {
		g.logger.Debug("openrouter product generation skipped: empty input")
		return ProductPreviewData{}, errors.New("input is required")
	}

	request := aiChatCompletionRequest{
		Model: g.model,
		Messages: []aiChatMessage{
			{Role: "system", Content: []aiChatContentPart{{Type: "text", Text: systemPrompt}}},
			{Role: "user", Content: []aiChatContentPart{{Type: "text", Text: userPrompt}}},
		},
	}
	g.logger.Debug("openrouter product request built", "model", g.model, "promptLength", len(userPrompt))

	start := time.Now()
	var response aiChatCompletionResponse
	err := g.client.Post(ctx, "chat/completions", request, &response)
	duration := time.Since(start)
	if err != nil {
		g.logger.Error("openrouter product request failed", "model", g.model, "duration", duration, "error", err)
		return ProductPreviewData{}, fmt.Errorf("openrouter request failed: %w", err)
	}
	g.logger.Debug("openrouter product response received", "model", g.model, "duration", duration, "choicesCount", len(response.Choices))
	if len(response.Choices) == 0 {
		g.logger.Error("openrouter returned no choices", "model", g.model, "duration", duration)
		return ProductPreviewData{}, errors.New("openrouter returned no choices")
	}
	if duration > 10*time.Second {
		g.logger.Warn("openrouter product request was slow", "model", g.model, "duration", duration)
	}

	content := response.Choices[0].Message.Content
	g.logger.Debug("openrouter product response content received", "model", g.model, "contentLength", len(content))

	parsed, err := parseAIProductResponse(content)
	if err != nil {
		g.logger.Error("openrouter product response parse failed", "model", g.model, "content", content, "error", err)
		return ProductPreviewData{}, err
	}
	g.logger.Debug("openrouter product response parsed", "model", g.model, "name", parsed.GeneralizedName, "kcals", parsed.Kcals)
	return parsed, nil
}

func parseAIProductResponse(raw string) (ProductPreviewData, error) {
	payload, err := extractJSON(raw)
	if err != nil {
		return ProductPreviewData{}, err
	}

	var parsed aiProductResponse
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		return ProductPreviewData{}, fmt.Errorf("parse ai json: %w", err)
	}

	name := strings.TrimSpace(parsed.GeneralizedName)
	if name == "" {
		name = strings.TrimSpace(parsed.Name)
	}
	if name == "" || strings.TrimSpace(parsed.Description) == "" {
		return ProductPreviewData{}, errors.New("ai response is incomplete")
	}
	if parsed.Kcals == nil || parsed.Protein == nil || parsed.Fat == nil || parsed.Carbs == nil || parsed.Fiber == nil {
		return ProductPreviewData{}, errors.New("ai response is missing nutrition fields")
	}

	result := ProductPreviewData{
		GeneralizedName: name,
		Kcals:           int64(*parsed.Kcals + 0.5),
		Protein:         roundOneDecimal(*parsed.Protein),
		Fat:             roundOneDecimal(*parsed.Fat),
		Carbs:           roundOneDecimal(*parsed.Carbs),
		Fiber:           roundOneDecimal(*parsed.Fiber),
		Description:     strings.TrimSpace(parsed.Description),
		Confidence:      0.9,
	}
	if err := validatePreviewData(result); err != nil {
		return ProductPreviewData{}, err
	}
	return result, nil
}

// trailingThinkTagPattern strips everything up to and including the last "</think>" marker.
// Reasoning ("thinking") models on OpenRouter (Qwen's thinking variants, DeepSeek-R1, etc.) emit
// a chain-of-thought before the actual JSON answer. The opening <think> tag is often swallowed by
// the provider before it reaches us, but the closing </think> tag reliably marks where the
// reasoning ends and the real answer begins, so matching is anchored on that alone. The greedy
// `.*` lands on the last occurrence if more than one shows up. Models that never think out loud
// have no </think> at all, so nothing is stripped and the old plain-JSON/fenced-JSON path runs
// unchanged — this keeps working the moment a non-reasoning model is swapped back in.
var trailingThinkTagPattern = regexp.MustCompile(`(?is)\A.*</think>\s*`)

func extractJSON(raw string) (string, error) {
	trimmed := stripCodeFence(trailingThinkTagPattern.ReplaceAllString(raw, ""))

	if json.Valid([]byte(trimmed)) {
		return trimmed, nil
	}

	if candidate, ok := lastBalancedJSONObject(trimmed); ok {
		return candidate, nil
	}

	return "", errors.New("no valid json found in ai response")
}

func stripCodeFence(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if !strings.HasPrefix(trimmed, "```") {
		return trimmed
	}
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	return strings.TrimSpace(trimmed)
}

// lastBalancedJSONObject scans for brace-balanced {...} spans and returns the last one that is
// itself valid JSON. This replaces a naive first-'{'-to-last-'}' match, which breaks the moment any
// stray brace shows up ahead of the real answer (leftover reasoning text, a worked example echoed
// back) — such a match spans from the stray brace all the way to the real closing brace and is
// neither valid JSON nor a useful error. Depth-tracking keeps each candidate self-contained, and
// picking the last valid one favors the final answer over anything earlier in the text.
func lastBalancedJSONObject(text string) (string, bool) {
	var (
		candidate string
		found     bool
		depth     int
		start     int
	)
	for i, r := range text {
		switch r {
		case '{':
			if depth == 0 {
				start = i
			}
			depth++
		case '}':
			if depth == 0 {
				continue
			}
			depth--
			if depth == 0 {
				span := text[start : i+1]
				if json.Valid([]byte(span)) {
					candidate = span
					found = true
				}
			}
		}
	}
	return candidate, found
}

func roundOneDecimal(value float64) float64 {
	return float64(int(value*10+0.5)) / 10
}

func validatePreviewData(data ProductPreviewData) error {
	if strings.TrimSpace(data.GeneralizedName) == "" || strings.TrimSpace(data.Description) == "" {
		return errors.New("preview data is incomplete")
	}
	if data.Kcals < 0 || data.Kcals > 1000 {
		return errors.New("preview kcals out of range")
	}
	if data.Protein < 0 || data.Protein > 100 || data.Fat < 0 || data.Fat > 100 || data.Carbs < 0 || data.Carbs > 100 || data.Fiber < 0 || data.Fiber > 50 {
		return errors.New("preview nutrition out of range")
	}
	return nil
}

const productGenerationSystemPrompt = "You analyze food descriptions and return one generalized Russian product with nutrition. Respond with JSON only. Fields: generalizedName, kcals, protein, fat, carbs, fiber, description. Use Russian for generalizedName and description. Generalize brands and variants to the core product. Keep values realistic per 100g."

const productGenerationUserPrompt = "Generate a new generalized food candidate from this user query: %s"
