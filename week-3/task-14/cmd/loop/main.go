// Команда loop прогоняет задачи через цикл «генерация → проверка → security
// review → коммит» и сводит результат в три колонки: что нашёл security step,
// что нашёл gateway, что пропустили оба.
//
// Запускать из корня репозитория:
//
//	go run ./week-3/task-14/cmd/loop -dry-run
//	go run ./week-3/task-14/cmd/loop -note "базовый прогон"
//	go run ./week-3/task-14/cmd/loop -base-url http://localhost:8787 -note "через gateway"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-3/task-14/internal/agent"
	"github.com/swanden/ai-challenge-advanced/week-3/task-14/internal/llm"
	"github.com/swanden/ai-challenge-advanced/week-3/task-14/internal/pipeline"
)

// Task — задача из набора.
type Task struct {
	ID       string   `json:"id"`
	Task     string   `json:"task"`
	Provokes []string `json:"provokes"`
	Why      string   `json:"why"`
}

// Report — сохраняемый результат прогона.
type Report struct {
	At        string             `json:"at"`
	Provider  string             `json:"provider"`
	Model     string             `json:"model"`
	BaseURL   string             `json:"base_url"`
	ViaProxy  bool               `json:"via_gateway"`
	MaxRounds int                `json:"max_rounds"`
	Note      string             `json:"note"`
	Outcomes  []pipeline.Outcome `json:"outcomes"`
}

func main() {
	var (
		tasksPath = flag.String("tasks", "week-3/task-14/dataset/tasks.jsonl", "набор задач")
		workspace = flag.String("workspace", "week-3/task-14/workspace", "рабочий каталог")
		outDir    = flag.String("out", "week-3/task-14/evidence", "куда положить отчёт")
		only      = flag.String("only", "", "только эти id задач, через запятую")
		maxRounds = flag.Int("rounds", 3, "потолок кругов на задачу")

		provider    = flag.String("provider", "anthropic", "anthropic или openai")
		model       = flag.String("model", "claude-sonnet-5", "имя модели")
		baseURL     = flag.String("base-url", "", "адрес API; укажи адрес gateway, чтобы пустить вызовы через него")
		keyEnv      = flag.String("key-env", "ANTHROPIC_API_KEY", "переменная с ключом")
		temperature = flag.Float64("temp", -1, "температура; отрицательное — поле не отправлять")
		maxTokens   = flag.Int("max-tokens", 8000, "потолок ответа; генерация файла на Go легко занимает больше 3000")

		skipReview = flag.Bool("skip-review", false,
			"пропустить security review — для показа того, что коммит всё равно не состоится")
		dryRun = flag.Bool("dry-run", false, "показать план и выйти")
		note   = flag.String("note", "", "пометка в отчёте")
	)
	flag.Parse()

	tasks, err := loadTasks(*tasksPath)
	if err != nil {
		fail("%v", err)
	}
	if *only != "" {
		tasks = filterTasks(tasks, strings.Split(*only, ","))
		if len(tasks) == 0 {
			fail("по фильтру -only не осталось задач")
		}
	}
	if *dryRun {
		fmt.Printf("задач %d, потолок кругов %d, вызовов модели до %d\n",
			len(tasks), *maxRounds, len(tasks)*(*maxRounds)*2)
		for _, t := range tasks {
			fmt.Printf("  %-11s провоцирует: %s\n", t.ID, strings.Join(t.Provokes, ", "))
		}
		return
	}

	key, err := llm.LoadKey(*keyEnv)
	if err != nil {
		fail("%v", err)
	}
	opts := llm.Options{BaseURL: *baseURL, APIKey: key, Model: *model, MaxTokens: *maxTokens}
	if *temperature >= 0 {
		t := *temperature
		opts.Temperature = &t
	}
	var c llm.Client
	switch *provider {
	case "anthropic":
		c = llm.NewAnthropic(opts)
	case "openai":
		c = llm.NewOpenAI(opts)
	default:
		fail("неизвестный провайдер %q", *provider)
	}

	if err := os.MkdirAll(*workspace, 0o755); err != nil {
		fail("рабочий каталог: %v", err)
	}

	rep := Report{
		At: time.Now().Format(time.RFC3339), Provider: c.Provider(), Model: c.Model(),
		BaseURL: *baseURL, ViaProxy: *baseURL != "" && !strings.Contains(*baseURL, "api.anthropic.com"),
		MaxRounds: *maxRounds, Note: *note,
	}

	ctx := context.Background()
	for _, t := range tasks {
		fmt.Fprintf(os.Stderr, "задача %s…\n", t.ID)
		o := pipeline.Run(ctx, c, t.ID, t.Task, pipeline.Options{
			MaxRounds: *maxRounds, Workspace: *workspace, SkipReview: *skipReview,
		})
		rep.Outcomes = append(rep.Outcomes, o)
	}

	print(os.Stdout, rep, tasks)
	if path, err := save(rep, *outDir); err == nil {
		fmt.Printf("\nОтчёт: %s\n", path)
	} else {
		fmt.Fprintf(os.Stderr, "отчёт не сохранён: %v\n", err)
	}
}

func print(w *os.File, rep Report, tasks []Task) {
	fmt.Fprintf(w, "\nМодель: %s (%s), потолок кругов %d", rep.Model, rep.Provider, rep.MaxRounds)
	if rep.ViaProxy {
		fmt.Fprintf(w, ", вызовы через gateway %s", rep.BaseURL)
	}
	if rep.Note != "" {
		fmt.Fprintf(w, "\nПометка: %s", rep.Note)
	}
	fmt.Fprint(w, "\n")

	fmt.Fprintf(w, "\nИтог по задачам\n")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "задача\tкругов\tхудший вердикт\tкоммит\tпричина\n")
	for _, o := range rep.Outcomes {
		worst := "—"
		if n := len(o.Rounds); n > 0 {
			worst = string(o.Rounds[n-1].Worst)
		}
		mark := "нет"
		if o.Committed {
			mark = "да"
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\n", o.TaskID, len(o.Rounds), worst, mark, o.Reason)
	}
	tw.Flush()

	// ---- три колонки, которых требует задание
	fmt.Fprintf(w, "\nЧто нашёл security step, что нашёл сканер, что пропустили оба\n")
	fmt.Fprintf(w, "Сравнение по первому кругу каждой задачи — до того, как замечания учтены.\n")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "задача\tправило\tsecurity review\tдетерминированный сканер\n")
	for _, o := range rep.Outcomes {
		if len(o.Rounds) == 0 {
			continue
		}
		first := o.Rounds[0]
		byReview := map[string]string{}
		for _, f := range first.Review.Findings {
			byReview[f.Rule] = string(f.Severity)
		}
		byScan := map[string]int{}
		for _, h := range first.Verify.Scan {
			byScan[h.Rule]++
		}
		rules := union(byReview, byScan)
		if len(rules) == 0 {
			fmt.Fprintf(tw, "%s\t—\tничего\tничего\n", o.TaskID)
			continue
		}
		for _, r := range rules {
			rev := "—"
			if v, ok := byReview[r]; ok {
				rev = v
			}
			scan := "—"
			if n, ok := byScan[r]; ok {
				scan = fmt.Sprintf("%d совпадений", n)
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", o.TaskID, r, rev, scan)
		}
	}
	tw.Flush()

	// ---- круги
	fmt.Fprintf(w, "\nПо кругам\n")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "задача\tкруг\tсобрался\tvet\tвердикт\tрешение\n")
	for _, o := range rep.Outcomes {
		for _, r := range o.Rounds {
			fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n", o.TaskID, r.N,
				yes(r.Verify.Compiles), yes(r.Verify.VetClean), or(string(r.Worst), "—"), r.Decision)
		}
	}
	tw.Flush()

	// ---- расхождение текста и вердикта
	fmt.Fprintf(w, "\nРасхождение прозы ревью с выставленным уровнем\n")
	shown := false
	for _, o := range rep.Outcomes {
		for _, r := range o.Rounds {
			if r.Review.Verdict != "" && r.Review.Verdict.Rank() < r.Worst.Rank() {
				fmt.Fprintf(w, "  %s круг %d: verdict=%s, но среди находок есть %s\n",
					o.TaskID, r.N, r.Review.Verdict, r.Worst)
				shown = true
			}
		}
	}
	if !shown {
		fmt.Fprintln(w, "  не обнаружено")
	}
}

func union(a map[string]string, b map[string]int) []string {
	seen := map[string]bool{}
	var out []string
	for k := range a {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for k := range b {
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

func yes(b bool) string {
	if b {
		return "да"
	}
	return "нет"
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func save(rep Report, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, time.Now().Format("20060102-150405")+"-loop.json")
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, b, 0o644)
}

func loadTasks(path string) ([]Task, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Task
	for i, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var t Task
		if err := json.Unmarshal([]byte(line), &t); err != nil {
			return nil, fmt.Errorf("%s строка %d: %w", path, i+1, err)
		}
		if t.ID == "" || strings.TrimSpace(t.Task) == "" {
			return nil, fmt.Errorf("%s строка %d: пустой id или task", path, i+1)
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: ни одной задачи", path)
	}
	return out, nil
}

func filterTasks(all []Task, ids []string) []Task {
	want := map[string]bool{}
	for _, id := range ids {
		want[strings.TrimSpace(id)] = true
	}
	var out []Task
	for _, t := range all {
		if want[t.ID] {
			out = append(out, t)
		}
	}
	return out
}

var _ = agent.Clean

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
	fmt.Fprintln(os.Stderr)
	os.Exit(1)
}
