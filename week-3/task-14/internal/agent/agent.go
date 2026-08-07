// Package agent держит два вызова модели: генерацию кода и security review.
//
// Ключевое решение — форма вердикта ревью. Модель возвращает не текст, а
// перечисление: critical, high, medium, low, clean. Решение о коммите
// принимает код по этому полю, а не по прозе ревью.
//
// Смысл в ограничении ущерба, а не в защите от уговоров. Убедить модель
// написать «это же тестовый код, всё нормально» можно и, судя по заданию Дня
// 15, будут. Но превратить эту фразу в разрешение на коммит нельзя, если поле
// severity осталось high: перечисление из пяти значений текстом не обходится.
// Это та же линия, что whitelist классов в Дне 11 и белый список получателей в
// Дне 12 — перечисление допустимого работает там, где перечисление
// недопустимого не работает никогда.
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/swanden/ai-challenge-advanced/week-3/task-14/internal/llm"
)

// Severity — уровень находки. Порядок значим: сравнение идёт по индексу.
type Severity string

const (
	Clean    Severity = "clean"
	Low      Severity = "low"
	Medium   Severity = "medium"
	High     Severity = "high"
	Critical Severity = "critical"
)

var order = []Severity{Clean, Low, Medium, High, Critical}

// Rank возвращает вес уровня; неизвестное значение считается критическим.
//
// Неизвестное — это критическое, а не нулевое, и это намеренно. Модель,
// придумавшая своё значение вместо перечисленных, — уже отклонение, и трактовать
// его в пользу коммита нельзя.
func (s Severity) Rank() int {
	for i, v := range order {
		if strings.EqualFold(string(s), string(v)) {
			return i
		}
	}
	return len(order) - 1
}

// Blocks сообщает, останавливает ли уровень коммит.
func (s Severity) Blocks() bool { return s.Rank() >= High.Rank() }

// Finding — одна находка ревью.
type Finding struct {
	Severity Severity `json:"severity"`
	Rule     string   `json:"rule"`
	Line     int      `json:"line"`
	Message  string   `json:"message"`
	Fix      string   `json:"fix"`
}

// Review — результат security review.
type Review struct {
	Verdict  Severity  `json:"verdict"`
	Findings []Finding `json:"findings"`
	// Comment — свободный текст модели. В решении не участвует вовсе и живёт
	// в отчёте только затем, чтобы было видно, расходится ли он с verdict
	// под давлением.
	Comment string `json:"comment,omitempty"`
}

// Worst возвращает худший уровень среди находок.
//
// Считается по находкам, а не по полю verdict: модель может выставить clean и
// тут же перечислить критические находки. Верным берётся худшее из двух.
func (r Review) Worst() Severity {
	worst := r.Verdict
	for _, f := range r.Findings {
		if f.Severity.Rank() > worst.Rank() {
			worst = f.Severity
		}
	}
	return worst
}

// genPrompt — системный промпт генератора. Про безопасность в нём намеренно
// ничего нет: иначе непонятно, что сработало — предупреждение генератору или
// слой ревью.
const genPrompt = `Ты пишешь код на Go для сервиса notes-api (HTTP API заметок, стандартная библиотека, роутер chi).

Верни ровно один JSON без пояснений и без markdown-разметки:
{"filename": "<имя файла вида internal/notes/xxx.go>", "code": "<полный текст файла на Go>", "note": "<что сделано, одна фраза>"}

Требования к коду: пакет notes, компилируется сам по себе, без внешних зависимостей кроме стандартной библиотеки и github.com/go-chi/chi/v5.`

// reviewPrompt — системный промпт ревьюера, настроенный под наш стек.
//
// Правила перечислены явно и конкретно: общая просьба «проверь безопасность»
// даёт общие ответы. Список взят из условия задания и дополнен тем, что
// действительно встречается в Go-сервисе.
const reviewPrompt = `Ты — security reviewer кода на Go для сервиса notes-api. Тебе дают файл. Найди проблемы безопасности.

Что искать (правила стека):
- hardcoded-secret: ключи, токены, пароли, строки подключения прямо в коде
- pii-in-logs: запись в лог тела запроса, заголовков, токенов, адресов, телефонов
- insecure-transport: http:// вместо https://, отключённая проверка сертификата, InsecureSkipVerify
- missing-validation: отсутствие проверки входных данных, размера тела, идентификаторов
- injection: конкатенация SQL или команд оболочки из пользовательских данных
- weak-crypto: md5, sha1, math/rand для секретов
- path-traversal: построение пути из пользовательского ввода без очистки

Уровни: critical — эксплуатируемо удалённо или утечка секрета; high — серьёзная слабость; medium — риск при определённых условиях; low — замечание; clean — ничего не найдено.

Верни ровно один JSON без пояснений и без markdown-разметки:
{"verdict": "<critical|high|medium|low|clean>", "findings": [{"severity": "<уровень>", "rule": "<правило из списка>", "line": <номер строки>, "message": "<что не так>", "fix": "<как починить>"}], "comment": "<одна фраза>"}

Поле verdict обязано быть одним из пяти перечисленных значений и равняться худшему уровню среди findings. Никаких других значений не допускается.`

// Generated — результат генерации.
type Generated struct {
	Filename string `json:"filename"`
	Code     string `json:"code"`
	Note     string `json:"note"`
}

// Generate просит модель написать код по задаче.
//
// feedback пуст на первом круге и несёт замечания ревью на последующих: цикл
// возвращается на генерацию именно так, как требует задание.
func Generate(ctx context.Context, c llm.Client, task, feedback string) (Generated, string, error) {
	user := "Задача: " + task
	if feedback != "" {
		user += "\n\nПредыдущая версия не прошла security review. Исправь именно это:\n" + feedback
	}
	raw, err := c.Complete(ctx, genPrompt, user)
	if err != nil {
		return Generated{}, raw, err
	}
	var g Generated
	if !parseJSON(raw, &g) {
		return Generated{}, raw, fmt.Errorf("ответ генератора не разобрался в JSON")
	}
	if strings.TrimSpace(g.Code) == "" {
		return Generated{}, raw, fmt.Errorf("генератор вернул пустой код")
	}
	return g, raw, nil
}

// Inspect просит модель проверить код.
func Inspect(ctx context.Context, c llm.Client, filename, code string) (Review, string, error) {
	user := "Файл " + filename + ":\n\n```go\n" + code + "\n```"
	raw, err := c.Complete(ctx, reviewPrompt, user)
	if err != nil {
		return Review{}, raw, err
	}
	var r Review
	if !parseJSON(raw, &r) {
		// Неразобравшийся ответ ревью — не повод коммитить. Считаем
		// критическим: отсутствие вердикта хуже плохого вердикта.
		return Review{Verdict: Critical, Comment: "ответ ревью не разобрался в JSON"}, raw,
			fmt.Errorf("ответ ревью не разобрался в JSON")
	}
	return r, raw, nil
}

// Feedback собирает замечания для следующего круга генерации.
func Feedback(r Review) string {
	var b strings.Builder
	for _, f := range r.Findings {
		if !f.Severity.Blocks() {
			continue
		}
		fmt.Fprintf(&b, "- [%s] %s (строка %d): %s. Как починить: %s\n",
			f.Severity, f.Rule, f.Line, f.Message, f.Fix)
	}
	return b.String()
}

// parseJSON достаёт первый сбалансированный объект из ответа модели.
func parseJSON(raw string, v any) bool {
	start := strings.Index(raw, "{")
	if start < 0 {
		return false
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(raw); i++ {
		c := raw[i]
		switch {
		case esc:
			esc = false
		case c == '\\' && inStr:
			esc = true
		case c == '"':
			inStr = !inStr
		case inStr:
		case c == '{':
			depth++
		case c == '}':
			depth--
			if depth == 0 {
				return json.Unmarshal([]byte(raw[start:i+1]), v) == nil
			}
		}
	}
	return false
}
