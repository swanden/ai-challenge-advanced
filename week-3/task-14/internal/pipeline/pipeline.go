// Package pipeline — цикл «генерация → проверка → security review → коммит».
//
// Главное свойство здесь не в порядке шагов, а в том, что **коммит физически
// не может произойти без пройденного ревью**. Функция Commit требует вердикт
// аргументом и отказывает, если его нет или он блокирующий. Не «агент должен
// вызвать проверку», а «без проверки нечего передать в коммит».
//
// Это ответ на вопрос, поставленный в чате курса: сабагента-ревьюера можно не
// вызвать, инструмент можно обойти через shell, а делать обе проверки — двойной
// расход. Ответ Гладкова был «детерминированные инструменты, условно prehook
// git push», и здесь он реализован буквально: обойти нечего, потому что нет
// пути к коммиту в обход.
package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-3/task-14/internal/agent"
	"github.com/swanden/ai-challenge-advanced/week-3/task-14/internal/llm"
	"github.com/swanden/ai-challenge-advanced/week-3/task-14/internal/verify"
)

// Round — один круг цикла.
type Round struct {
	N         int            `json:"n"`
	Filename  string         `json:"filename"`
	Code      string         `json:"-"`
	CodeLines int            `json:"code_lines"`
	Verify    verify.Result  `json:"verify"`
	Review    agent.Review   `json:"review"`
	Worst     agent.Severity `json:"worst"`
	Decision  string         `json:"decision"`
	// RawOnError — сырой ответ модели, сохраняемый только когда разбор не
	// удался. Без него причину провала нельзя установить по evidence, а
	// именно провалы и надо уметь разбирать. При успехе не пишется: ответы
	// крупные, и хранить их все незачем.
	RawOnError string `json:"raw_on_error,omitempty"`
	GenRaw     string `json:"-"`
	ReviewRaw  string `json:"-"`
	DurationMs int64  `json:"duration_ms"`
	Error      string `json:"error,omitempty"`
}

// Outcome — итог по задаче.
type Outcome struct {
	TaskID    string  `json:"task_id"`
	Task      string  `json:"task"`
	Rounds    []Round `json:"rounds"`
	Committed bool    `json:"committed"`
	Path      string  `json:"path,omitempty"`
	Reason    string  `json:"reason,omitempty"`
}

// Options — настройки прогона.
type Options struct {
	MaxRounds  int
	Workspace  string
	SkipReview bool // только для показа: см. комментарий в Run
}

// Run прогоняет одну задачу через цикл.
func Run(ctx context.Context, c llm.Client, taskID, task string, opt Options) Outcome {
	out := Outcome{TaskID: taskID, Task: task}
	feedback := ""

	for n := 1; n <= opt.MaxRounds; n++ {
		start := time.Now()
		r := Round{N: n}

		gen, genRaw, err := agent.Generate(ctx, c, task, feedback)
		r.GenRaw = genRaw
		if err != nil {
			r.Error = err.Error()
			r.RawOnError = tail(genRaw, 600)
			r.Decision = "ошибка генерации"
			r.DurationMs = time.Since(start).Milliseconds()
			out.Rounds = append(out.Rounds, r)
			break
		}
		r.Filename, r.Code = gen.Filename, gen.Code
		r.CodeLines = len(strings.Split(gen.Code, "\n"))

		r.Verify = verify.Check(ctx, opt.Workspace, gen.Filename, gen.Code)
		if r.Verify.Blocking() {
			// Не компилируется — на ревью не идём, возвращаемся с ошибкой
			// сборки. Это тот же цикл, что требует задание, только повод
			// другой.
			r.Decision = "не компилируется, круг заново"
			feedback = "Код не компилируется:\n" + strings.Join(r.Verify.Errors, "\n")
			r.DurationMs = time.Since(start).Milliseconds()
			out.Rounds = append(out.Rounds, r)
			continue
		}

		if !opt.SkipReview {
			review, reviewRaw, err := agent.Inspect(ctx, c, gen.Filename, gen.Code)
			r.Review, r.ReviewRaw = review, reviewRaw
			if err != nil {
				r.Error = err.Error()
				r.RawOnError = tail(reviewRaw, 600)
			}
		}
		r.Worst = r.Review.Worst()
		r.DurationMs = time.Since(start).Milliseconds()

		switch {
		case opt.SkipReview:
			r.Decision = "ревью пропущено флагом — коммит запрещён"
		case r.Worst.Blocks():
			r.Decision = "найдено " + string(r.Worst) + ", круг заново"
			feedback = agent.Feedback(r.Review)
		case r.Worst.Rank() > agent.Clean.Rank():
			r.Decision = "только " + string(r.Worst) + " — коммит с предупреждением"
		default:
			r.Decision = "чисто — коммит"
		}
		out.Rounds = append(out.Rounds, r)

		if opt.SkipReview || r.Worst.Blocks() {
			continue
		}
		path, err := Commit(opt.Workspace, taskID, gen.Filename, gen.Code, &r.Review)
		if err != nil {
			out.Reason = err.Error()
			break
		}
		out.Committed, out.Path = true, path
		return out
	}

	if !out.Committed && out.Reason == "" {
		out.Reason = fmt.Sprintf("исчерпаны %d круга без чистого ревью", opt.MaxRounds)
	}
	return out
}

// tail возвращает хвост строки: обрыв по лимиту виден именно в конце.
func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

// Commit кладёт код в рабочий каталог — и только если вердикт позволяет.
//
// Вердикт передаётся аргументом, а не читается откуда-то: вызвать коммит, не
// имея результата ревью, невозможно на уровне сигнатуры. nil означает «ревью не
// было» и отвергается наравне с блокирующим уровнем.
//
// Это и есть тот детерминированный контроль, о котором спорили в чате: не
// инструкция агенту вызвать проверку, а отсутствие пути в обход неё.
func Commit(workspace, taskID, filename, code string, review *agent.Review) (string, error) {
	if review == nil {
		return "", fmt.Errorf("коммит отклонён: ревью не проводилось")
	}
	if w := review.Worst(); w.Blocks() {
		return "", fmt.Errorf("коммит отклонён: вердикт %s", w)
	}
	dir := filepath.Join(workspace, "committed", taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Base(filename)
	if !strings.HasSuffix(name, ".go") {
		name += ".go"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(code), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
