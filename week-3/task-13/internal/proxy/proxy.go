// Package proxy — HTTP-прокси между приложением и провайдером модели.
//
// Прокси намеренно **прозрачный**: тело запроса пересылается как есть, а
// трогаются только текстовые поля по известным для формата местам. Формат
// определяется по пути: /v1/messages — Anthropic, /v1/chat/completions —
// OpenAI-совместимый.
//
// Так сделано ради того, чтобы раннеры Дней 11 и 12 пошли через прокси без
// единой правки — достаточно подменить -base-url. Смысл гейтвея как раз в
// одной точке контроля: вызовы модели разбросаны по коду, и проверка внутри
// одного из них остальных не покрывает. Это же и способ доказать, что прокси
// работает на настоящем трафике, а не только на тестах.
package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-3/task-13/internal/audit"
	"github.com/swanden/ai-challenge-advanced/week-3/task-13/internal/detect"
	"github.com/swanden/ai-challenge-advanced/week-3/task-13/internal/policy"
)

// Config — настройки прокси.
type Config struct {
	AnthropicBase string
	OpenAIBase    string
	InputMode     policy.Mode
	OutputMode    policy.Mode
	RatePerMinute int
	Log           *audit.Log
	Client        *http.Client
}

// Gateway — сам прокси.
type Gateway struct {
	cfg     Config
	limiter *audit.Limiter
}

// New собирает прокси.
func New(cfg Config) *Gateway {
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 120 * time.Second}
	}
	if cfg.AnthropicBase == "" {
		cfg.AnthropicBase = "https://api.anthropic.com"
	}
	return &Gateway{cfg: cfg, limiter: audit.NewLimiter(cfg.RatePerMinute)}
}

// Handler возвращает маршрутизатор.
func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/messages", g.handle)
	mux.HandleFunc("/v1/chat/completions", g.handle)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, g.cfg.Log.Stats())
	})
	return mux
}

// format описывает, где в теле лежит текст.
type format struct {
	name     string
	upstream string
	path     string
}

func (g *Gateway) formatFor(path string) format {
	if strings.HasSuffix(path, "/chat/completions") {
		return format{name: "openai", upstream: g.cfg.OpenAIBase, path: "/chat/completions"}
	}
	return format{name: "anthropic", upstream: g.cfg.AnthropicBase, path: "/v1/messages"}
}

func (g *Gateway) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "только POST"})
		return
	}
	start := time.Now()
	id := audit.RequestID()
	client := clientOf(r)

	if ok, _ := g.limiter.Allow(client); !ok {
		g.cfg.Log.Write(audit.Entry{RequestID: id, Client: client, Path: r.URL.Path,
			Verdict: "rate-limited", Error: "превышен предел запросов в минуту"})
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"gateway": map[string]string{"verdict": "rate-limited",
				"message": "превышен предел запросов в минуту"}})
		return
	}

	raw, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "тело не разобралось: " + err.Error()})
		return
	}

	f := g.formatFor(r.URL.Path)
	model, _ := body["model"].(string)

	// --- входной слой
	texts := collectTexts(body)
	var findings []detect.Finding
	blocked := false
	// safe — обезвреженные тексты для журнала. Собираются отдельно от тех,
	// что уходят в модель, и это принципиально: при блокировке подмена в
	// теле запроса не выполняется (тело никуда не идёт), а в журнал писать
	// исходный текст всё равно нельзя.
	//
	// Первая версия этого не делала, и заблокированный ключ попал в журнал
	// открытым текстом — ровно та ошибка, которую до нас совершил dpmn и
	// про которую здесь же написано в комментариях. Ловится только
	// проверкой `probe -check-logs`, глазами не видно.
	safe := make([]string, len(texts))
	for i := range texts {
		v := policy.ScanInput(*texts[i].ptr, g.cfg.InputMode)
		findings = append(findings, tag(v.Findings, texts[i].where)...)
		_, cleaned := detect.Scan(*texts[i].ptr)
		safe[i] = cleaned
		switch v.Action {
		case "blocked":
			blocked = true
		case "masked":
			*texts[i].ptr = v.Text
		}
	}

	systemText := systemOf(body)
	preview := audit.Preview(strings.Join(safe, " "), 300)

	if blocked {
		g.cfg.Log.Write(audit.Entry{RequestID: id, Client: client, Path: r.URL.Path,
			Model: model, Verdict: "blocked", Input: findings,
			PromptPreview: preview, LatencyMs: time.Since(start).Milliseconds()})
		writeJSON(w, http.StatusForbidden, map[string]any{
			"gateway": map[string]any{
				"verdict":  "blocked",
				"message":  "запрос заблокирован и в модель не отправлен: " + detect.Describe(findings),
				"findings": findings,
			}})
		return
	}

	// --- пересылка
	out, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		strings.TrimRight(f.upstream, "/")+f.path, bytes.NewReader(out))
	if err != nil {
		g.fail(w, id, client, r.URL.Path, model, err, start)
		return
	}
	copyHeaders(r, req)

	resp, err := g.cfg.Client.Do(req)
	if err != nil {
		g.fail(w, id, client, r.URL.Path, model, err, start)
		return
	}
	payload, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		g.fail(w, id, client, r.URL.Path, model, err, start)
		return
	}
	if resp.StatusCode != http.StatusOK {
		g.cfg.Log.Write(audit.Entry{RequestID: id, Client: client, Path: r.URL.Path,
			Model: model, Verdict: "upstream-error", Input: findings,
			PromptPreview: preview, Error: fmt.Sprintf("HTTP %d", resp.StatusCode),
			LatencyMs: time.Since(start).Milliseconds()})
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(resp.StatusCode)
		w.Write(payload)
		return
	}

	// --- выходной слой
	var upstream map[string]any
	if err := json.Unmarshal(payload, &upstream); err != nil {
		g.fail(w, id, client, r.URL.Path, model, err, start)
		return
	}
	answer := answerOf(upstream, f.name)
	ov := policy.ScanOutput(answer, systemText, g.cfg.OutputMode)
	verdict := "pass"
	if len(findings) > 0 {
		verdict = "masked"
	}
	switch ov.Action {
	case "blocked":
		verdict = "blocked"
		setAnswer(upstream, f.name, "[Ответ заблокирован LLM Gateway: "+detect.Describe(ov.Findings)+"]")
	case "masked":
		verdict = "masked"
		setAnswer(upstream, f.name, ov.Text)
	}

	in, outTok := usageOf(upstream, f.name)
	cost, src := audit.Cost(model, in, outTok)
	upstream["gateway"] = map[string]any{
		"verdict": verdict, "input_findings": findings, "output_findings": ov.Findings,
		"prompt_tokens": in, "output_tokens": outTok,
		"cost_usd": cost, "price_source": src,
	}

	g.cfg.Log.Write(audit.Entry{RequestID: id, Client: client, Path: r.URL.Path,
		Model: model, Verdict: verdict, Input: findings, Output: ov.Findings,
		PromptPreview: preview, PromptTokens: in, OutputTokens: outTok,
		CostUSD: cost, Upstream: f.upstream, LatencyMs: time.Since(start).Milliseconds()})

	writeJSON(w, http.StatusOK, upstream)
}

func (g *Gateway) fail(w http.ResponseWriter, id, client, path, model string, err error, start time.Time) {
	g.cfg.Log.Write(audit.Entry{RequestID: id, Client: client, Path: path, Model: model,
		Verdict: "error", Error: err.Error(), LatencyMs: time.Since(start).Milliseconds()})
	writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
}

// textRef — ссылка на текстовое поле внутри разобранного тела.
type textRef struct {
	ptr   *string
	where string
}

// collectTexts находит все текстовые поля обоих форматов.
//
// У Anthropic system лежит отдельным полем и может быть строкой или списком
// блоков; у обоих форматов содержимое сообщения — строка либо список блоков.
// Разбираются все варианты, потому что пропущенное поле означает дыру в
// проверке, а не неудобство.
func collectTexts(body map[string]any) []textRef {
	var refs []textRef

	switch s := body["system"].(type) {
	case string:
		v := s
		refs = append(refs, textRef{ptr: &v, where: "system"})
		body["system"] = &v
	case []any:
		for i, blk := range s {
			if m, ok := blk.(map[string]any); ok {
				refs = append(refs, blockRef(m, fmt.Sprintf("system[%d]", i))...)
			}
		}
	}

	msgs, _ := body["messages"].([]any)
	for i, item := range msgs {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		where := fmt.Sprintf("messages[%d].%s", i, role)
		switch c := m["content"].(type) {
		case string:
			v := c
			refs = append(refs, textRef{ptr: &v, where: where})
			m["content"] = &v
		case []any:
			for j, blk := range c {
				if bm, ok := blk.(map[string]any); ok {
					refs = append(refs, blockRef(bm, fmt.Sprintf("%s[%d]", where, j))...)
				}
			}
		}
	}
	return refs
}

func blockRef(m map[string]any, where string) []textRef {
	s, ok := m["text"].(string)
	if !ok {
		return nil
	}
	v := s
	m["text"] = &v
	return []textRef{{ptr: &v, where: where}}
}

func systemOf(body map[string]any) string {
	switch s := body["system"].(type) {
	case string:
		return s
	case *string:
		return *s
	}
	return ""
}

func tag(findings []detect.Finding, where string) []detect.Finding {
	for i := range findings {
		findings[i].Where = where
	}
	return findings
}

func answerOf(resp map[string]any, format string) string {
	if format == "anthropic" {
		blocks, _ := resp["content"].([]any)
		var b strings.Builder
		for _, item := range blocks {
			if m, ok := item.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					b.WriteString(t)
				}
			}
		}
		return b.String()
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	c, _ := choices[0].(map[string]any)
	m, _ := c["message"].(map[string]any)
	s, _ := m["content"].(string)
	return s
}

func setAnswer(resp map[string]any, format, text string) {
	if format == "anthropic" {
		resp["content"] = []any{map[string]any{"type": "text", "text": text}}
		return
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		return
	}
	if c, ok := choices[0].(map[string]any); ok {
		if m, ok := c["message"].(map[string]any); ok {
			m["content"] = text
		}
	}
}

func usageOf(resp map[string]any, format string) (int, int) {
	u, _ := resp["usage"].(map[string]any)
	if u == nil {
		return 0, 0
	}
	if format == "anthropic" {
		return num(u["input_tokens"]), num(u["output_tokens"])
	}
	return num(u["prompt_tokens"]), num(u["completion_tokens"])
}

func num(v any) int {
	if f, ok := v.(float64); ok {
		return int(f)
	}
	return 0
}

// copyHeaders переносит заголовки авторизации.
//
// Ключ проходит насквозь и на прокси не оседает. Так проще: иначе гейтвей
// становится хранилищем секретов, то есть новой мишенью.
func copyHeaders(from *http.Request, to *http.Request) {
	to.Header.Set("content-type", "application/json")
	for _, h := range []string{"x-api-key", "authorization", "anthropic-version"} {
		if v := from.Header.Get(h); v != "" {
			to.Header.Set(h, v)
		}
	}
	if to.Header.Get("anthropic-version") == "" && strings.Contains(to.URL.Path, "messages") {
		to.Header.Set("anthropic-version", "2023-06-01")
	}
}

func clientOf(r *http.Request) string {
	if v := r.Header.Get("x-forwarded-for"); v != "" {
		return strings.TrimSpace(strings.Split(v, ",")[0])
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	return host
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}
