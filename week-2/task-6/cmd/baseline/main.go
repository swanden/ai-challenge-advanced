// Команда baseline прогоняет примеры из eval через базовую модель БЕЗ файнтюна
// и фиксирует ответы — это точка отсчёта, с которой сравнивается дообученная модель.
//
// Промпт берётся из internal/dataset, то есть ровно тот же, каким размечен
// датасет. Ничего не подсказываем: список классов модель видит, определений
// классов — нет.
//
// Выборка послойная. Примеры в eval отсортированы по идентификатору, поэтому
// первые N подряд дают перекос: при N=10 и восьми классах в выборку попадают
// только пять первых по алфавиту. Вместо этого классы обходятся по кругу —
// сначала по одному примеру каждого, потом по второму, и так далее.
//
// Отчёт прогона пишется всегда, в том числе когда API отвечает отказом на
// середине: потраченные вызовы не должны пропадать вместе с процессом.
//
// Работает с любым OpenAI-совместимым эндпоинтом:
//
//	go run ./week-2/task-6/cmd/baseline -model gpt-4o-mini -n 10
//	go run ./week-2/task-6/cmd/baseline -base-url http://localhost:11434/v1 -key-env "" -model qwen2.5:7b
//	go run ./week-2/task-6/cmd/baseline -base-url https://router.huggingface.co/v1 -key-env HF_TOKEN -model Qwen/Qwen2.5-7B-Instruct
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-2/task-6/internal/dataset"
	"github.com/swanden/ai-challenge-advanced/week-2/task-6/internal/envfile"
)

type result struct {
	Index      int    `json:"index"`
	User       string `json:"user"`
	Expected   string `json:"expected"`
	Raw        string `json:"raw"`       // что модель ответила дословно
	Predicted  string `json:"predicted"` // что из этого удалось распознать
	Correct    bool   `json:"correct"`
	WellFormed bool   `json:"well_formed"` // ответ — ровно один класс, без лишнего
	LatencyMS  int64  `json:"latency_ms"`
	Prompt     int    `json:"prompt_tokens"`
	Completion int    `json:"completion_tokens"`
}

type failure struct {
	Index    int    `json:"index"`
	Expected string `json:"expected"`
	Error    string `json:"error"`
}

type run struct {
	Model               string    `json:"model"`
	BaseURL             string    `json:"base_url"`
	StartedAt           time.Time `json:"started_at"`
	System              string    `json:"system_prompt"`
	Requested           int       `json:"requested"`
	Note                string    `json:"note,omitempty"`
	Results             []result  `json:"results"`
	Failures            []failure `json:"failures,omitempty"`
	Accuracy            float64   `json:"accuracy"`
	FormatOK            float64   `json:"format_compliance"`
	AvgLatency          int64     `json:"avg_latency_ms"`
	AvgCompletionTokens float64   `json:"avg_completion_tokens"`
}

func main() {
	evalPath := flag.String("eval", "week-2/task-6/dataset/eval.jsonl", "путь к eval.jsonl")
	n := flag.Int("n", 10, "сколько примеров прогнать; 0 — все")
	model := flag.String("model", "gpt-4o-mini", "модель")
	baseURL := flag.String("base-url", "https://api.openai.com/v1", "базовый URL OpenAI-совместимого API")
	keyEnv := flag.String("key-env", "OPENAI_API_KEY", "переменная окружения с ключом; пустая строка — без авторизации")
	tokensField := flag.String("tokens-field", "max_tokens", "имя поля лимита ответа: max_tokens или max_completion_tokens")
	outDir := flag.String("out", "week-2/task-6/evidence", "куда положить отчёт прогона")
	envPath := flag.String("env-file", "", "файл окружения; по умолчанию ищется .env в текущем каталоге и выше")
	note := flag.String("note", "", "пометка, которая уйдёт в отчёт прогона")
	flag.Parse()

	if path, err := envfile.Load(*envPath); err != nil {
		fail("%v", err)
	} else if path != "" {
		fmt.Printf("переменные окружения прочитаны из %s\n", path)
	}

	all, err := dataset.LoadExamples(*evalPath)
	if err != nil {
		fail("%v", err)
	}
	examples := stratify(all, *n)

	var key string
	if *keyEnv != "" {
		key = os.Getenv(*keyEnv)
		if key == "" {
			fail("переменная %s пуста — положите ключ в .env или передайте -key-env \"\"", *keyEnv)
		}
	}

	client := &http.Client{Timeout: 60 * time.Second}
	r := run{
		Model:     *model,
		BaseURL:   *baseURL,
		StartedAt: time.Now(),
		System:    dataset.SystemPrompt,
		Requested: len(examples),
		Note:      *note,
	}

	fmt.Printf("прогон %d примеров: %s\n\n", len(examples), classPlan(examples))

	stopped := false
	for i, ex := range examples {
		user := ex.Messages[1].Content
		expected := ex.Messages[2].Content

		start := time.Now()
		raw, promptTok, complTok, err := ask(client, *baseURL, key, *model, *tokensField, user)
		latency := time.Since(start).Milliseconds()
		if err != nil {
			r.Failures = append(r.Failures, failure{Index: i + 1, Expected: expected, Error: err.Error()})
			fmt.Fprintf(os.Stderr, "! пример %d (%s): %v\n", i+1, expected, err)
			stopped = true
			break
		}

		pred, wellFormed := parseAnswer(raw)
		res := result{
			Index: i + 1, User: user, Expected: expected,
			Raw: raw, Predicted: pred, Correct: pred == expected,
			WellFormed: wellFormed, LatencyMS: latency,
			Prompt: promptTok, Completion: complTok,
		}
		r.Results = append(r.Results, res)

		mark := "×"
		if res.Correct {
			mark = "✓"
		}
		fmt.Printf("%s %-16s ожидалось %-16s ответ %q\n", mark, pred, expected, truncate(raw, 60))
	}

	summarize(&r)
	printSummary(r)
	if path := save(r, *outDir); path != "" {
		fmt.Printf("\nотчёт: %s\n", path)
	}
	if stopped {
		fmt.Fprintln(os.Stderr, "\nпрогон остановлен на ошибке; выполненные вызовы сохранены в отчёте")
		os.Exit(1)
	}
}

// stratify отбирает примеры, обходя классы по кругу, чтобы в выборке из
// N примеров были представлены все классы, а не первые по алфавиту.
func stratify(all []dataset.Example, n int) []dataset.Example {
	if n <= 0 || n >= len(all) {
		return all
	}
	byClass := map[string][]dataset.Example{}
	for _, ex := range all {
		cls := ex.Messages[2].Content
		byClass[cls] = append(byClass[cls], ex)
	}
	classes := make([]string, 0, len(byClass))
	for cls := range byClass {
		classes = append(classes, cls)
	}
	sort.Strings(classes)

	out := make([]dataset.Example, 0, n)
	for round := 0; ; round++ {
		progressed := false
		for _, cls := range classes {
			items := byClass[cls]
			if round >= len(items) {
				continue
			}
			progressed = true
			out = append(out, items[round])
			if len(out) == n {
				return out
			}
		}
		if !progressed {
			return out
		}
	}
}

// classPlan показывает состав выборки до прогона — чтобы перекос был виден
// сразу, а не после того, как вызовы потрачены.
func classPlan(examples []dataset.Example) string {
	counts := map[string]int{}
	for _, ex := range examples {
		counts[ex.Messages[2].Content]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	return strings.Join(parts, ", ")
}

// ask отправляет один запрос в чат-комплишен и возвращает текст ответа.
func ask(c *http.Client, baseURL, key, model, tokensField, user string) (string, int, int, error) {
	body := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": dataset.SystemPrompt},
			{"role": "user", "content": user},
		},
		"temperature": 0,
		tokensField:   16,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", 0, 0, err
	}
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(baseURL, "/")+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return "", 0, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", 0, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, 0, err
	}
	if resp.StatusCode/100 != 2 {
		return "", 0, 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}
	var parsed struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", 0, 0, fmt.Errorf("не разобрал ответ: %w", err)
	}
	if len(parsed.Choices) == 0 {
		return "", 0, 0, fmt.Errorf("пустой список choices")
	}
	return parsed.Choices[0].Message.Content, parsed.Usage.PromptTokens, parsed.Usage.CompletionTokens, nil
}

// parseAnswer вытаскивает класс из ответа модели.
//
// Возвращает второй результат false, если ответ пришлось разбирать:
// требование задания — одно слово, и всё, что сверх того, считается
// нарушением формата, даже когда класс угадан верно.
func parseAnswer(raw string) (string, bool) {
	s := strings.TrimSpace(strings.ToLower(raw))
	s = strings.Trim(s, "`\"'.!, \n\t")
	if dataset.IsClass(s) {
		return s, true
	}
	classes := dataset.SortedClasses()
	sort.Slice(classes, func(i, j int) bool { return len(classes[i]) > len(classes[j]) })
	for _, c := range classes {
		if strings.Contains(s, c) {
			return c, false
		}
	}
	return "?", false
}

func summarize(r *run) {
	if len(r.Results) == 0 {
		return
	}
	var correct, wellFormed int
	var totalLatency int64
	var totalCompletion int
	for _, res := range r.Results {
		if res.Correct {
			correct++
		}
		if res.WellFormed {
			wellFormed++
		}
		totalLatency += res.LatencyMS
		totalCompletion += res.Completion
	}
	n := float64(len(r.Results))
	r.Accuracy = float64(correct) / n
	r.FormatOK = float64(wellFormed) / n
	r.AvgLatency = totalLatency / int64(len(r.Results))
	r.AvgCompletionTokens = float64(totalCompletion) / n
}

func printSummary(r run) {
	if len(r.Results) == 0 {
		fmt.Println("\nни одного успешного вызова")
		return
	}
	fmt.Printf("\nмодель %s\n", r.Model)
	fmt.Printf("точность %.0f%% (%d из %d выполненных)\n", r.Accuracy*100, countCorrect(r), len(r.Results))
	if len(r.Failures) > 0 {
		fmt.Printf("не выполнено вызовов: %d из запрошенных %d\n", len(r.Failures), r.Requested)
	}
	fmt.Printf("формат соблюдён в %.0f%% ответов\n", r.FormatOK*100)
	fmt.Printf("средняя задержка %d мс, средняя длина ответа %.1f токена\n", r.AvgLatency, r.AvgCompletionTokens)

	type miss struct{ from, to string }
	misses := map[miss]int{}
	for _, res := range r.Results {
		if !res.Correct {
			misses[miss{res.Expected, res.Predicted}]++
		}
	}
	if len(misses) == 0 {
		return
	}
	fmt.Println("ошибки:")
	keys := make([]miss, 0, len(misses))
	for k := range misses {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].from != keys[j].from {
			return keys[i].from < keys[j].from
		}
		return keys[i].to < keys[j].to
	})
	for _, k := range keys {
		fmt.Printf("  %s → %s: %d\n", k.from, k.to, misses[k])
	}
}

func countCorrect(r run) int {
	n := 0
	for _, res := range r.Results {
		if res.Correct {
			n++
		}
	}
	return n
}

// save пишет отчёт независимо от того, чем закончился прогон.
func save(r run, outDir string) string {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "baseline: отчёт не сохранён: %v\n", err)
		return ""
	}
	name := fmt.Sprintf("baseline-%s-%s.json",
		strings.NewReplacer("/", "_", ":", "_").Replace(r.Model),
		r.StartedAt.Format("20060102-150405"))
	path := filepath.Join(outDir, name)
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "baseline: отчёт не сохранён: %v\n", err)
		return ""
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "baseline: отчёт не сохранён: %v\n", err)
		return ""
	}
	return path
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "baseline: "+format+"\n", args...)
	os.Exit(1)
}
