// Package audit пишет журнал, считает стоимость и ограничивает частоту.
//
// Правило, которому подчинён весь пакет: **в журнале не должно быть ни одного
// секрета.** Это не общая осторожность, а конкретный урок из чужой работы: у
// dpmn первая версия гейтвея писала заблокированный запрос в аудит-лог с ключом
// открытым текстом. Прокси, поставленный ради предотвращения утечки, устраивал
// её сам, и заметили это только проверкой grep-ом по логу.
//
// Поэтому наружу отдаётся только обезвреженный текст, и есть команда `probe
// -check-logs`, которая ищет в журналах известные тестовые секреты.
package audit

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-3/task-13/internal/detect"
)

// Entry — одна строка журнала.
type Entry struct {
	At        string           `json:"at"`
	RequestID string           `json:"request_id"`
	Client    string           `json:"client"`
	Path      string           `json:"path"`
	Model     string           `json:"model"`
	Verdict   string           `json:"verdict"`
	Input     []detect.Finding `json:"input_findings,omitempty"`
	Output    []detect.Finding `json:"output_findings,omitempty"`
	// PromptPreview — начало обезвреженного запроса. Именно обезвреженного:
	// исходный текст сюда не попадает ни при каких обстоятельствах.
	PromptPreview string  `json:"prompt_preview,omitempty"`
	PromptTokens  int     `json:"prompt_tokens"`
	OutputTokens  int     `json:"output_tokens"`
	CostUSD       float64 `json:"cost_usd"`
	LatencyMs     int64   `json:"latency_ms"`
	Upstream      string  `json:"upstream,omitempty"`
	Error         string  `json:"error,omitempty"`
}

// Log — журнал с накопительными счётчиками.
type Log struct {
	mu    sync.Mutex
	dir   string
	stats Stats
}

// Stats — счётчики за время работы.
type Stats struct {
	Total        int     `json:"total"`
	Passed       int     `json:"passed"`
	Masked       int     `json:"masked"`
	Blocked      int     `json:"blocked"`
	Errors       int     `json:"errors"`
	PromptTokens int     `json:"prompt_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// New заводит журнал в каталоге.
func New(dir string) (*Log, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Log{dir: dir}, nil
}

// Write добавляет строку в журнал за сегодняшний день.
func (l *Log) Write(e Entry) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	e.At = time.Now().Format(time.RFC3339)
	l.stats.Total++
	switch e.Verdict {
	case "blocked":
		l.stats.Blocked++
	case "masked":
		l.stats.Masked++
	case "pass":
		l.stats.Passed++
	}
	if e.Error != "" {
		l.stats.Errors++
	}
	l.stats.PromptTokens += e.PromptTokens
	l.stats.OutputTokens += e.OutputTokens
	l.stats.CostUSD += e.CostUSD

	path := filepath.Join(l.dir, "gateway-"+time.Now().Format("2006-01-02")+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	b, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(b, '\n'))
	return err
}

// Stats возвращает копию счётчиков.
func (l *Log) Stats() Stats {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.stats
}

// ---------------------------------------------------------------- стоимость

// Price — цена за миллион токенов.
type Price struct {
	InputPerMTok  float64
	OutputPerMTok float64
}

// Prices — прайс по моделям.
//
// Цифры зашиты и потому устаревают. Рядом с любой суммой в отчёте обязана
// стоять пометка об источнике: у dpmn ровно здесь оказалось слабое место —
// единицу цены он вывел по порядку величины и подтверждения в документации не
// нашёл. Лучше явная пометка, чем красивое число неизвестного происхождения.
var Prices = map[string]Price{
	"claude-sonnet-5":           {InputPerMTok: 3, OutputPerMTok: 15},
	"claude-opus-5":             {InputPerMTok: 15, OutputPerMTok: 75},
	"claude-haiku-4-5-20251001": {InputPerMTok: 0.8, OutputPerMTok: 4},
}

// Cost считает стоимость вызова. Для локальных моделей возвращает ноль.
func Cost(model string, in, out int) (float64, string) {
	p, ok := Prices[model]
	if !ok {
		return 0, "прайс неизвестен"
	}
	c := float64(in)/1e6*p.InputPerMTok + float64(out)/1e6*p.OutputPerMTok
	return c, "зашитый прайс, проверить перед использованием в отчётности"
}

// ---------------------------------------------------- ограничение частоты

// Limiter — ограничитель по клиенту, скользящее окно в минуту.
type Limiter struct {
	mu    sync.Mutex
	limit int
	hits  map[string][]time.Time
}

// NewLimiter заводит ограничитель. Нулевой предел означает «без ограничения».
func NewLimiter(perMinute int) *Limiter {
	return &Limiter{limit: perMinute, hits: map[string][]time.Time{}}
}

// Allow сообщает, пропускать ли запрос, и сколько осталось в окне.
func (l *Limiter) Allow(client string) (bool, int) {
	if l.limit <= 0 {
		return true, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	cutoff := time.Now().Add(-time.Minute)
	var kept []time.Time
	for _, t := range l.hits[client] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.hits[client] = kept
		return false, 0
	}
	kept = append(kept, time.Now())
	l.hits[client] = kept
	return true, l.limit - len(kept)
}

// Preview обрезает текст для журнала.
func Preview(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// RequestID — идентификатор запроса для сопоставления строк журнала.
func RequestID() string {
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}
