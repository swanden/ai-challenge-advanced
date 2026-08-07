// Package llm — клиенты к моделям.
//
// Клиентов два, и это не дублирование ради дублирования. У Anthropic в
// /v1/messages системный промпт лежит в отдельном поле system, а не первым
// сообщением в общем списке. Именно эта разница даёт контрольный прогон
// -system-in-user: те же атаки при правилах в отдельном поле и при правилах в
// обычном сообщении. В OpenAI-совместимом API такого разделения нет, там
// system — просто сообщение с ролью, и вопрос ставить негде.
//
// Второй клиент нужен для контрастной модели через Ollama: разница между
// промптом-жертвой и защищённым видна только там, где жертву вообще удаётся
// сломать.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Client — минимальный интерфейс, которого хватает замеру.
type Client interface {
	Complete(ctx context.Context, system, user string) (string, error)
	Model() string
	Provider() string
}

// Options — общие настройки клиента.
//
// Temperature — указатель, а не число, и это не украшательство. Часть моделей
// Anthropic отвергает запрос с этим полем целиком: «temperature is deprecated
// for this model», HTTP 400. Отличить «температура ноль» от «поле не
// отправлять» нулевым значением нельзя, поэтому nil означает второе.
type Options struct {
	BaseURL     string
	APIKey      string
	Model       string
	Temperature *float64
	MaxTokens   int
	Timeout     time.Duration
	Retries     int
}

// TempLabel описывает температуру для отчёта.
func (o Options) TempLabel() string {
	if o.Temperature == nil {
		return "не задана"
	}
	return fmt.Sprintf("%.1f", *o.Temperature)
}

func (o Options) withDefaults() Options {
	if o.MaxTokens == 0 {
		o.MaxTokens = 400
	}
	if o.Timeout == 0 {
		o.Timeout = 90 * time.Second
	}
	if o.Retries == 0 {
		o.Retries = 3
	}
	return o
}

// ---------------------------------------------------------------- Anthropic

// Anthropic — клиент к /v1/messages.
type Anthropic struct {
	opt  Options
	http *http.Client
}

// NewAnthropic собирает клиента.
func NewAnthropic(o Options) *Anthropic {
	o = o.withDefaults()
	if o.BaseURL == "" {
		o.BaseURL = "https://api.anthropic.com"
	}
	return &Anthropic{opt: o, http: &http.Client{Timeout: o.Timeout}}
}

func (c *Anthropic) Model() string    { return c.opt.Model }
func (c *Anthropic) Provider() string { return "anthropic" }

type anthropicRequest struct {
	Model       string             `json:"model"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature,omitempty"`
	System      string             `json:"system,omitempty"`
	Messages    []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// Complete отправляет один запрос. Пустой system означает, что правила уехали
// в пользовательское сообщение — поле в этом случае не отправляется вовсе,
// иначе контрольный прогон измерял бы не то, что заявлено.
func (c *Anthropic) Complete(ctx context.Context, system, user string) (string, error) {
	body := anthropicRequest{
		Model:       c.opt.Model,
		MaxTokens:   c.opt.MaxTokens,
		Temperature: c.opt.Temperature,
		System:      system,
		Messages:    []anthropicMessage{{Role: "user", Content: user}},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	var lastErr error
	for attempt := 0; attempt < c.opt.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.opt.BaseURL, "/")+"/v1/messages", bytes.NewReader(raw))
		if err != nil {
			return "", err
		}
		req.Header.Set("content-type", "application/json")
		req.Header.Set("x-api-key", c.opt.APIKey)
		req.Header.Set("anthropic-version", "2023-06-01")

		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		payload, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(payload))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(payload))
		}
		var out anthropicResponse
		if err := json.Unmarshal(payload, &out); err != nil {
			return "", fmt.Errorf("разбор ответа: %w", err)
		}
		if out.Error != nil {
			return "", fmt.Errorf("%s: %s", out.Error.Type, out.Error.Message)
		}
		var sb strings.Builder
		for _, blk := range out.Content {
			if blk.Type == "text" {
				sb.WriteString(blk.Text)
			}
		}
		// Обрезанный по лимиту ответ надо отличать от неверного. Иначе
		// оборванный на середине JSON выглядит как «модель ответила криво»,
		// и чинить будут не то. Ровно так и вышло в первом прогоне Дня 14.
		if out.StopReason == "max_tokens" {
			return sb.String(), fmt.Errorf("ответ обрезан по лимиту max_tokens (%d): увеличьте -max-tokens", c.opt.MaxTokens)
		}
		return sb.String(), nil
	}
	return "", fmt.Errorf("не удалось после %d попыток: %w", c.opt.Retries, lastErr)
}

// ------------------------------------------------------------------ OpenAI

// OpenAI — клиент к OpenAI-совместимому /chat/completions. Через него ходим в
// Ollama за контрастной моделью.
type OpenAI struct {
	opt  Options
	http *http.Client
}

// NewOpenAI собирает клиента.
func NewOpenAI(o Options) *OpenAI {
	o = o.withDefaults()
	if o.BaseURL == "" {
		o.BaseURL = "http://localhost:11434/v1"
	}
	return &OpenAI{opt: o, http: &http.Client{Timeout: o.Timeout}}
}

func (c *OpenAI) Model() string    { return c.opt.Model }
func (c *OpenAI) Provider() string { return "openai" }

type openAIRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	Temperature *float64        `json:"temperature,omitempty"`
	MaxTokens   int             `json:"max_tokens"`
}

type openAIMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIResponse struct {
	Choices []struct {
		Message openAIMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Complete отправляет один запрос.
func (c *OpenAI) Complete(ctx context.Context, system, user string) (string, error) {
	msgs := make([]openAIMessage, 0, 2)
	if strings.TrimSpace(system) != "" {
		msgs = append(msgs, openAIMessage{Role: "system", Content: system})
	}
	msgs = append(msgs, openAIMessage{Role: "user", Content: user})

	raw, err := json.Marshal(openAIRequest{
		Model:       c.opt.Model,
		Messages:    msgs,
		Temperature: c.opt.Temperature,
		MaxTokens:   c.opt.MaxTokens,
	})
	if err != nil {
		return "", err
	}

	var lastErr error
	for attempt := 0; attempt < c.opt.Retries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.opt.BaseURL, "/")+"/chat/completions", bytes.NewReader(raw))
		if err != nil {
			return "", err
		}
		req.Header.Set("content-type", "application/json")
		if c.opt.APIKey != "" {
			req.Header.Set("authorization", "Bearer "+c.opt.APIKey)
		}
		resp, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		payload, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(payload))
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, snippet(payload))
		}
		var out openAIResponse
		if err := json.Unmarshal(payload, &out); err != nil {
			return "", fmt.Errorf("разбор ответа: %w", err)
		}
		if out.Error != nil {
			return "", fmt.Errorf("ошибка API: %s", out.Error.Message)
		}
		if len(out.Choices) == 0 {
			return "", fmt.Errorf("пустой список choices")
		}
		return out.Choices[0].Message.Content, nil
	}
	return "", fmt.Errorf("не удалось после %d попыток: %w", c.opt.Retries, lastErr)
}

// -------------------------------------------------------------------- ключ

// LoadKey достаёт ключ: сначала из окружения, потом из .env в корне
// репозитория. Пустое имя переменной означает, что ключ не нужен вовсе — так
// ходим в локальную Ollama.
func LoadKey(envName string) (string, error) {
	if envName == "" {
		return "", nil
	}
	if v := strings.TrimSpace(os.Getenv(envName)); v != "" {
		return v, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(dir, ".env")
		if b, err := os.ReadFile(path); err == nil {
			for _, line := range strings.Split(string(b), "\n") {
				line = strings.TrimSpace(line)
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				k, v, ok := strings.Cut(line, "=")
				if !ok || strings.TrimSpace(k) != envName {
					continue
				}
				v = strings.TrimSpace(v)
				v = strings.Trim(v, `"'`)
				if v != "" {
					return v, nil
				}
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("переменная %s не найдена ни в окружении, ни в .env", envName)
}

func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}
