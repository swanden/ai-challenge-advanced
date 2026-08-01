// Package spec достаёт контракт классификации — системный промпт и список
// допустимых классов — из самого датасета Дня 6.
//
// Почему так, а не общей константой в коде. Пакет в internal/ виден только
// внутри дерева, начинающегося с родителя этого internal, поэтому
// week-2/task-6/internal/dataset из task-7 не импортируется: Go запрещает.
// Остаются три выхода — переехать общим пакетом на уровень недели, скопировать
// константы сюда или прочитать их из данных.
//
// Выбран третий. Копия промпта в двух местах разошлась бы молча, и Дни 6 и 7
// начали бы измерять разное без единой ошибки компиляции. А чтение из данных
// даёт гарантию строже общей константы: даже если кто-то поправит константу в
// коде Дня 6, замер Дня 7 всё равно пойдёт тем промптом, каким датасет реально
// размечен. Данные здесь главнее кода, и это правильный порядок.
package spec

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// Message — сообщение в формате чат-комплишена.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Example — строка train.jsonl или eval.jsonl.
type Example struct {
	Messages []Message `json:"messages"`
}

// User возвращает формулировку задачи.
func (e Example) User() string { return e.Messages[1].Content }

// Class возвращает эталонный класс.
func (e Example) Class() string { return e.Messages[2].Content }

// Line — сырая строка файла с номером, чтобы ошибку можно было найти.
type Line struct {
	No  int
	Raw string
}

// Spec — контракт, восстановленный из датасета.
type Spec struct {
	SystemPrompt string
	Classes      []string
	Source       string // из какого файла взят
}

// ReadLines читает файл и отдаёт непустые строки с номерами.
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

// LoadExamples читает файл примеров и проверяет структуру каждой строки.
func LoadExamples(path string) ([]Example, error) {
	lines, err := ReadLines(path)
	if err != nil {
		return nil, err
	}
	out := make([]Example, 0, len(lines))
	for _, ln := range lines {
		var ex Example
		if err := json.Unmarshal([]byte(ln.Raw), &ex); err != nil {
			return nil, fmt.Errorf("%s строка %d: невалидный JSON: %w", path, ln.No, err)
		}
		if len(ex.Messages) != 3 {
			return nil, fmt.Errorf("%s строка %d: ожидалось 3 сообщения, получено %d", path, ln.No, len(ex.Messages))
		}
		for i, want := range []string{"system", "user", "assistant"} {
			if ex.Messages[i].Role != want {
				return nil, fmt.Errorf("%s строка %d: роль %q вместо %q", path, ln.No, ex.Messages[i].Role, want)
			}
			if strings.TrimSpace(ex.Messages[i].Content) == "" {
				return nil, fmt.Errorf("%s строка %d: пустой content у роли %s", path, ln.No, want)
			}
		}
		out = append(out, ex)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: ни одной строки", path)
	}
	return out, nil
}

// FromExamples восстанавливает контракт по примерам.
//
// Требует, чтобы системный промпт совпадал во всех строках побайтово: если он
// разъехался внутри самого датасета, дальше замерять нечего, и лучше упасть
// здесь, чем получить бессмысленные цифры.
func FromExamples(path string, examples []Example) (*Spec, error) {
	prompt := examples[0].Messages[0].Content
	classes := map[string]bool{}
	for i, ex := range examples {
		if ex.Messages[0].Content != prompt {
			return nil, fmt.Errorf("%s: системный промпт в примере %d отличается от первого", path, i+1)
		}
		classes[ex.Class()] = true
	}
	list := make([]string, 0, len(classes))
	for c := range classes {
		list = append(list, c)
	}
	sort.Strings(list)
	return &Spec{SystemPrompt: prompt, Classes: list, Source: path}, nil
}

// Load читает файл и восстанавливает контракт за один шаг.
func Load(path string) (*Spec, []Example, error) {
	examples, err := LoadExamples(path)
	if err != nil {
		return nil, nil, err
	}
	sp, err := FromExamples(path, examples)
	if err != nil {
		return nil, nil, err
	}
	return sp, examples, nil
}

// IsClass сообщает, входит ли строка в допустимое множество.
func (s *Spec) IsClass(v string) bool {
	for _, c := range s.Classes {
		if c == v {
			return true
		}
	}
	return false
}

// ParseClass вытаскивает класс из ответа модели. Второй результат говорит,
// был ли ответ ровно одним классом без лишнего.
func (s *Spec) ParseClass(raw string) (string, bool) {
	v := strings.TrimSpace(strings.ToLower(raw))
	v = strings.Trim(v, "`\"'.!,:; \n\t")
	if s.IsClass(v) {
		return v, true
	}
	// Длинные имена проверяем раньше коротких: иначе contract-change
	// не отличить от других по подстроке.
	byLen := append([]string(nil), s.Classes...)
	sort.Slice(byLen, func(i, j int) bool { return len(byLen[i]) > len(byLen[j]) })
	for _, c := range byLen {
		if strings.Contains(v, c) {
			return c, false
		}
	}
	return "", false
}

// List возвращает классы одной строкой — для промпта самопроверки.
func (s *Spec) List() string { return strings.Join(s.Classes, ", ") }

// PromptOrder возвращает классы в том порядке, в каком они перечислены внутри
// системного промпта. Это не то же самое, что Classes: там порядок
// алфавитный, а в промпте — авторский.
func (s *Spec) PromptOrder() []string {
	type pos struct {
		class string
		at    int
	}
	var found []pos
	for _, c := range s.Classes {
		if i := strings.Index(s.SystemPrompt, c); i >= 0 {
			found = append(found, pos{class: c, at: i})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].at < found[j].at })
	out := make([]string, 0, len(found))
	for _, f := range found {
		out = append(out, f.class)
	}
	return out
}

// PromptRotated возвращает системный промпт, в котором перечисление классов
// провёрнуто на k позиций.
//
// Нужно для слоя избыточности: если модель тянет к первому классу в списке,
// перестановка это вскроет, а температура — нет. При k, кратном числу
// классов, возвращается исходный промпт.
//
// Второй результат равен false, если перечисление в промпте не нашлось: тогда
// вызывающая сторона должна честно откатиться на другой способ выборки, а не
// делать вид, что провернула список.
func (s *Spec) PromptRotated(k int) (string, bool) {
	order := s.PromptOrder()
	n := len(order)
	if n < 2 || len(order) != len(s.Classes) {
		return s.SystemPrompt, false
	}
	start := strings.Index(s.SystemPrompt, order[0])
	last := order[n-1]
	end := strings.Index(s.SystemPrompt, last)
	if start < 0 || end < 0 {
		return s.SystemPrompt, false
	}
	end += len(last)
	if end <= start {
		return s.SystemPrompt, false
	}

	k = ((k % n) + n) % n
	if k == 0 {
		return s.SystemPrompt, true
	}
	rotated := make([]string, 0, n)
	for i := 0; i < n; i++ {
		rotated = append(rotated, order[(i+k)%n])
	}
	return s.SystemPrompt[:start] + strings.Join(rotated, ", ") + s.SystemPrompt[end:], true
}
