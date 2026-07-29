// Package dataset — единственный источник правды по формату датасета:
// системный промпт, список классов и строгий разбор JSONL.
//
// Системный промпт лежит здесь, а не в трёх местах, намеренно: baseline
// обязан спрашивать модель ровно тем же промптом, каким размечен датасет,
// иначе сравнение «до и после» ничего не значит.
package dataset

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// SystemPrompt перечисляет классы, но не объясняет их.
// Это осознанный выбор: если положить сюда определения, базовая модель
// получит знание из промпта и замерять будет нечего.
const SystemPrompt = "Ты маршрутизатор задач репозитория notes-api. Прочитай формулировку задачи и ответь ровно одним классом из списка: bug-fix, feature, refactor, testing, docs, research, review, contract-change. Ответ — одно слово, без пояснений."

// Classes — допустимое множество ответов.
var Classes = []string{
	"bug-fix",
	"feature",
	"refactor",
	"testing",
	"docs",
	"research",
	"review",
	"contract-change",
}

// IsClass сообщает, входит ли строка в допустимое множество.
func IsClass(s string) bool {
	for _, c := range Classes {
		if c == s {
			return true
		}
	}
	return false
}

// Record — строка master.jsonl: пример вместе с происхождением.
type Record struct {
	ID     string `json:"id"`
	Class  string `json:"class"`
	Real   bool   `json:"real"`
	Origin string `json:"origin"`
	User   string `json:"user"`
}

// Message — сообщение в формате чат-комплишена.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Example — строка train.jsonl или eval.jsonl.
type Example struct {
	Messages []Message `json:"messages"`
}

// Line — сырая строка файла вместе с номером, чтобы валидатор мог
// показать, где именно проблема.
type Line struct {
	No  int
	Raw string
}

// ReadLines читает файл и возвращает непустые строки с их номерами.
func ReadLines(path string) ([]Line, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Line
	for i, raw := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		out = append(out, Line{No: i + 1, Raw: raw})
	}
	return out, nil
}

// LoadMaster читает master.jsonl.
func LoadMaster(path string) ([]Record, error) {
	lines, err := ReadLines(path)
	if err != nil {
		return nil, err
	}
	recs := make([]Record, 0, len(lines))
	for _, ln := range lines {
		var r Record
		if err := json.Unmarshal([]byte(ln.Raw), &r); err != nil {
			return nil, fmt.Errorf("строка %d: %w", ln.No, err)
		}
		recs = append(recs, r)
	}
	return recs, nil
}

// ParseExample разбирает строку train/eval и проверяет её структуру:
// ровно три сообщения, роли в порядке system → user → assistant,
// содержимое непустое, ответ ассистента из допустимого множества.
func ParseExample(raw string) (Example, error) {
	var ex Example
	if err := json.Unmarshal([]byte(raw), &ex); err != nil {
		return ex, fmt.Errorf("невалидный JSON: %w", err)
	}
	if len(ex.Messages) != 3 {
		return ex, fmt.Errorf("ожидалось 3 сообщения, получено %d", len(ex.Messages))
	}
	want := []string{"system", "user", "assistant"}
	for i, w := range want {
		if ex.Messages[i].Role != w {
			return ex, fmt.Errorf("сообщение %d: роль %q вместо %q", i+1, ex.Messages[i].Role, w)
		}
		if strings.TrimSpace(ex.Messages[i].Content) == "" {
			return ex, fmt.Errorf("сообщение %d (%s): пустой content", i+1, w)
		}
	}
	if ex.Messages[0].Content != SystemPrompt {
		return ex, fmt.Errorf("системный промпт отличается от эталонного")
	}
	if !IsClass(ex.Messages[2].Content) {
		return ex, fmt.Errorf("ответ %q вне допустимого множества классов", ex.Messages[2].Content)
	}
	return ex, nil
}

// LoadExamples читает и проверяет весь файл примеров.
func LoadExamples(path string) ([]Example, error) {
	lines, err := ReadLines(path)
	if err != nil {
		return nil, err
	}
	out := make([]Example, 0, len(lines))
	for _, ln := range lines {
		ex, err := ParseExample(ln.Raw)
		if err != nil {
			return nil, fmt.Errorf("%s строка %d: %w", path, ln.No, err)
		}
		out = append(out, ex)
	}
	return out, nil
}

// NewExample собирает пример из формулировки и класса.
func NewExample(user, class string) Example {
	return Example{Messages: []Message{
		{Role: "system", Content: SystemPrompt},
		{Role: "user", Content: user},
		{Role: "assistant", Content: class},
	}}
}

// Normalize приводит текст к виду, пригодному для поиска дублей.
func Normalize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimRight(s, ".!?…")
}

// SortedClasses возвращает классы в алфавитном порядке — для стабильного вывода.
func SortedClasses() []string {
	out := append([]string(nil), Classes...)
	sort.Strings(out)
	return out
}
