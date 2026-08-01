// Package llm — клиент к OpenAI-совместимому чат-комплишену с поддержкой
// logprobs.
//
// Лежит на уровне недели, а не внутри задания, потому что нужен и Дню 7 для
// оценки уверенности, и Дню 8 для маршрутизации между моделями.
//
// От клиента в Дне 6 отличается двумя вещами: умеет просить вероятности
// токенов и умеет фиксировать seed, чтобы прогон с температурой оставался
// воспроизводимым.
package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Message — сообщение диалога.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Alternative — вариант токена с его логарифмической вероятностью.
type Alternative struct {
	Token   string  `json:"token"`
	LogProb float64 `json:"logprob"`
}

// Token — сгенерированный токен вместе с альтернативами, которые модель
// рассматривала на его месте.
type Token struct {
	Token   string        `json:"token"`
	LogProb float64       `json:"logprob"`
	Top     []Alternative `json:"top"`
}

// Prob возвращает обычную вероятность токена.
func (t Token) Prob() float64 { return math.Exp(t.LogProb) }

// Answer — ответ модели вместе с тем, что нужно для оценки уверенности.
type Answer struct {
	Raw              string  `json:"raw"`
	Tokens           []Token `json:"tokens,omitempty"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
	LatencyMS        int64   `json:"latency_ms"`
}

// SeqProb — вероятность всей последовательности: произведение вероятностей
// её токенов. Считается через сумму логарифмов, чтобы не терять точность
// на длинных ответах.
//
// Если вероятности не запрашивались или не пришли, возвращает -1: это
// отличимо от честного нуля.
func (a Answer) SeqProb() float64 {
	if len(a.Tokens) == 0 {
		return -1
	}
	sum := 0.0
	for _, t := range a.Tokens {
		sum += t.LogProb
	}
	return math.Exp(sum)
}

// Margin — отрыв первого токена ответа от ближайшей альтернативы на его
// месте, в вероятностях. Ноль означает, что модель колебалась между двумя
// вариантами, единица — что альтернатив не было.
//
// Отрыв обычно информативнее самой вероятности: ответ с вероятностью 0.55
// при втором варианте 0.05 куда надёжнее, чем 0.55 при втором 0.44.
func (a Answer) Margin() float64 {
	if len(a.Tokens) == 0 {
		return -1
	}
	first := a.Tokens[0]
	best, second := -1.0, -1.0
	for _, alt := range first.Top {
		p := math.Exp(alt.LogProb)
		if p > best {
			second = best
			best = p
			continue
		}
		if p > second {
			second = p
		}
	}
	if best < 0 {
		// Альтернатив не прислали — берём вероятность самого токена.
		return first.Prob()
	}
	if second < 0 {
		second = 0
	}
	return best - second
}

// Client — настроенный клиент одной модели.
type Client struct {
	HTTP        *http.Client
	BaseURL     string
	Key         string
	Model       string
	TokensField string // max_tokens или max_completion_tokens
}

// New собирает клиент с разумными значениями по умолчанию.
func New(baseURL, key, model string) *Client {
	return &Client{
		HTTP:        &http.Client{Timeout: 120 * time.Second},
		BaseURL:     baseURL,
		Key:         key,
		Model:       model,
		TokensField: "max_tokens",
	}
}

// Request — параметры одного обращения.
type Request struct {
	Messages    []Message
	Temperature float64
	MaxTokens   int
	LogProbs    bool
	TopLogProbs int
	Seed        int // 0 — не передавать
}

// Ask выполняет один запрос.
func (c *Client) Ask(req Request) (Answer, error) {
	var out Answer

	body := map[string]any{
		"model":       c.Model,
		"messages":    req.Messages,
		"temperature": req.Temperature,
	}
	if req.MaxTokens > 0 {
		body[c.TokensField] = req.MaxTokens
	}
	if req.LogProbs {
		body["logprobs"] = true
		if req.TopLogProbs > 0 {
			body["top_logprobs"] = req.TopLogProbs
		}
	}
	if req.Seed != 0 {
		body["seed"] = req.Seed
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return out, err
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return out, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if c.Key != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.Key)
	}

	start := time.Now()
	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	out.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		return out, err
	}
	if resp.StatusCode/100 != 2 {
		return out, fmt.Errorf("HTTP %d: %s", resp.StatusCode, cut(string(raw), 300))
	}

	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			LogProbs struct {
				Content []struct {
					Token       string        `json:"token"`
					LogProb     float64       `json:"logprob"`
					TopLogProbs []Alternative `json:"top_logprobs"`
				} `json:"content"`
			} `json:"logprobs"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return out, fmt.Errorf("не разобрал ответ: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return out, fmt.Errorf("пустой список choices")
	}

	out.Raw = parsed.Choices[0].Message.Content
	out.PromptTokens = parsed.Usage.PromptTokens
	out.CompletionTokens = parsed.Usage.CompletionTokens
	for _, t := range parsed.Choices[0].LogProbs.Content {
		out.Tokens = append(out.Tokens, Token{
			Token:   t.Token,
			LogProb: t.LogProb,
			Top:     t.TopLogProbs,
		})
	}
	return out, nil
}

func cut(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
