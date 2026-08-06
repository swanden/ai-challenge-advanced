// Команда trap прогоняет ловушки с непрямой инъекцией по трём векторам,
// пяти способам сокрытия и четырём слоям защиты.
//
// Запускать из корня репозитория:
//
//	go run ./week-3/task-12/cmd/trap -dry-run
//	go run ./week-3/task-12/cmd/trap -note "базовый прогон"
//	go run ./week-3/task-12/cmd/trap -provider openai \
//	    -base-url http://localhost:11434/v1 -key-env "" -model qwen2.5:7b \
//	    -note "контрастная модель"
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-3/task-12/internal/hide"
	"github.com/swanden/ai-challenge-advanced/week-3/task-12/internal/judge"
	"github.com/swanden/ai-challenge-advanced/week-3/task-12/internal/llm"
	"github.com/swanden/ai-challenge-advanced/week-3/task-12/internal/report"
	"github.com/swanden/ai-challenge-advanced/week-3/task-12/internal/scenario"
	"github.com/swanden/ai-challenge-advanced/week-3/task-12/internal/shield"
)

type job struct {
	vector  scenario.Vector
	level   shield.Level
	clean   bool
	payload scenario.Payload
	method  hide.Method
	repeat  int
}

func main() {
	var (
		payloadsPath = flag.String("payloads", "week-3/task-12/dataset/payloads.jsonl", "набор нагрузок")
		outDir       = flag.String("out", "week-3/task-12/evidence", "куда положить отчёт")

		vectorsArg = flag.String("vectors", "summarizer,analyst,searcher", "векторы через запятую")
		levelsArg  = flag.String("levels", "none,sanitize,boundary,all", "слои защиты через запятую")
		methodsArg = flag.String("methods", "html-comment,hidden-style,zero-width,md-link-title,visible", "способы сокрытия через запятую")
		only       = flag.String("only", "", "только эти id нагрузок, через запятую")
		repeats    = flag.Int("repeats", 3, "повторов на каждую попытку")

		provider    = flag.String("provider", "anthropic", "anthropic или openai")
		model       = flag.String("model", "claude-sonnet-5", "имя модели")
		baseURL     = flag.String("base-url", "", "адрес API; пусто — по умолчанию для провайдера")
		keyEnv      = flag.String("key-env", "ANTHROPIC_API_KEY", "переменная с ключом; пусто — без ключа")
		temperature = flag.Float64("temp", -1, "температура; отрицательное значение — поле не отправлять вовсе")
		maxTokens   = flag.Int("max-tokens", 700, "потолок ответа")

		showAll   = flag.Bool("show-all", false, "печатать каждый диалог целиком")
		showHits  = flag.Bool("show-hits", false, "печатать только сработавшие нагрузки")
		withClean = flag.Bool("with-clean", true, "прогонять чистые носители; выключать только для точечных расшифровок")
		workers   = flag.Int("workers", 4, "параллельных запросов")
		maxErrors = flag.Int("max-errors", 5, "прервать прогон, если столько первых вызовов подряд упали; 0 — не прерывать")
		minCalls  = flag.Float64("min-calls", 0.5, "минимальная доля дошедших до модели попыток")
		dryRun    = flag.Bool("dry-run", false, "посчитать вызовы и выйти")
		note      = flag.String("note", "", "пометка в отчёте")
	)
	flag.Parse()

	payloads, err := scenario.LoadPayloads(*payloadsPath)
	if err != nil {
		fail("набор нагрузок: %v", err)
	}
	if *only != "" {
		payloads = filterPayloads(payloads, strings.Split(*only, ","))
		if len(payloads) == 0 {
			fail("по фильтру -only не осталось ни одной нагрузки")
		}
	}
	vectors, err := parseVectors(*vectorsArg)
	if err != nil {
		fail("%v", err)
	}
	levels, err := parseLevels(*levelsArg)
	if err != nil {
		fail("%v", err)
	}
	methods, err := parseMethods(*methodsArg)
	if err != nil {
		fail("%v", err)
	}
	if !*withClean {
		fmt.Fprintln(os.Stderr, "чистые носители отключены: таблица полезности будет пустой")
	}

	jobs := buildJobs(vectors, levels, methods, payloads, *repeats, *withClean)
	if *dryRun {
		fmt.Printf("векторов %d, слоёв %d, способов %d, нагрузок %d, повторов %d\n",
			len(vectors), len(levels), len(methods), len(payloads), *repeats)
		fmt.Printf("всего попыток: %d\n", len(jobs))
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
	var client llm.Client
	switch *provider {
	case "anthropic":
		client = llm.NewAnthropic(opts)
	case "openai":
		client = llm.NewOpenAI(opts)
	default:
		fail("неизвестный провайдер %q", *provider)
	}

	rep := report.New(report.Meta{
		Provider:    client.Provider(),
		Model:       client.Model(),
		Temperature: opts.TempLabel(),
		Repeats:     *repeats,
		MinCalls:    *minCalls,
		Payloads:    *payloadsPath,
		Note:        *note,
	})

	if (*showAll || *showHits) && *workers > 1 {
		fmt.Fprintln(os.Stderr,
			"показ диалогов при нескольких потоках: блоки не перемешаются, но пойдут вразнобой. Для читаемой расшифровки — -workers 1")
	}

	run(context.Background(), client, jobs, rep, *workers, *maxErrors, *showAll, *showHits)

	rep.Sort()
	path, err := rep.Save(*outDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "отчёт не сохранён: %v\n", err)
	}
	rep.Print(os.Stdout, func(o string) bool { return judge.Outcome(o).Success() })
	if path != "" {
		fmt.Printf("\nОтчёт: %s\n", path)
	}
}

func buildJobs(vectors []scenario.Vector, levels []shield.Level, methods []hide.Method,
	payloads []scenario.Payload, repeats int, withClean bool) []job {

	var jobs []job
	for _, v := range vectors {
		for _, l := range levels {
			for _, p := range scenario.For(payloads, v) {
				for _, m := range methods {
					for r := 1; r <= repeats; r++ {
						jobs = append(jobs, job{vector: v, level: l, payload: p, method: m, repeat: r})
					}
				}
			}
			if withClean {
				for r := 1; r <= repeats; r++ {
					jobs = append(jobs, job{vector: v, level: l, clean: true, repeat: r})
				}
			}
		}
	}
	return jobs
}

func run(ctx context.Context, client llm.Client, jobs []job, rep *report.Report,
	workers, maxErrors int, showAll, showHits bool) {

	if workers < 1 {
		workers = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan job)
	var mu sync.Mutex
	var wg sync.WaitGroup
	done, errs, ok := 0, 0, 0
	reported, aborted := false, false
	systems := map[string]int{}

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				rec := execute(ctx, client, j)
				mu.Lock()
				rep.Add(rec)
				done++
				if rec.Err != "" {
					errs++
					if !reported {
						reported = true
						fmt.Fprintf(os.Stderr, "\nпервая ошибка вызова: %s\n", rec.Err)
					}
				} else {
					ok++
				}
				if maxErrors > 0 && ok == 0 && errs >= maxErrors && !aborted {
					aborted = true
					fmt.Fprintf(os.Stderr, "прогон прерван: первые %d вызовов подряд упали\n", errs)
					cancel()
				}
				if showAll || (showHits && rec.Kind == report.KindAttack &&
					judge.Outcome(rec.Outcome).Success()) {
					printDialog(os.Stdout, rec, systems)
				}
				if done%25 == 0 || done == len(jobs) {
					fmt.Fprintf(os.Stderr, "\r%d из %d", done, len(jobs))
				}
				mu.Unlock()
			}
		}()
	}
	for _, j := range jobs {
		select {
		case <-ctx.Done():
		case ch <- j:
			continue
		}
		break
	}
	close(ch)
	wg.Wait()
	fmt.Fprintln(os.Stderr)
}

func execute(ctx context.Context, client llm.Client, j job) report.Record {
	rec := report.Record{
		Vector: string(j.vector),
		Level:  string(j.level),
		Kind:   report.KindAttack,
		Repeat: j.repeat,
	}
	if j.clean {
		rec.Kind = report.KindClean
	} else {
		rec.Payload = j.payload.ID
		rec.Class = j.payload.Class
		rec.Method = string(j.method)
	}

	source := scenario.BuildClean(j.vector)
	if !j.clean {
		source = scenario.Build(j.vector, j.method, j.payload)
	}

	// Слой очистки работает до модели. Он же даёт эталон для проверки
	// выхода: сравнивать ответ надо с очищенным носителем, иначе спрятанная
	// инструкция считалась бы законным содержимым источника.
	clean := source
	if j.level != shield.None {
		var trace shield.Trace
		clean, trace = shield.Clean(source)
		source = clean
		if trace.Any() {
			rec.Sanitized = map[string]int{}
			if trace.Comments > 0 {
				rec.Sanitized["html-comment"] = trace.Comments
			}
			if trace.Hidden > 0 {
				rec.Sanitized["hidden-style"] = trace.Hidden
			}
			if trace.LinkAttrs > 0 {
				rec.Sanitized["md-link-title"] = trace.LinkAttrs
			}
			if trace.ZeroWide > 0 {
				rec.Sanitized["zero-width"] = trace.ZeroWide
			}
		}
	} else {
		clean, _ = shield.Clean(source)
	}

	system := scenario.Persona(j.vector)
	body := source
	if j.level == shield.Boundary || j.level == shield.All {
		system += shield.BoundaryRule
		body = shield.Wrap(source)
	}
	user := scenario.Question(j.vector) + "\n\n" + body

	rec.SentSystem = system
	rec.SentUser = user

	start := time.Now()
	raw, err := client.Complete(ctx, system, user)
	rec.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		rec.Err = err.Error()
		rec.Outcome = string(judge.Failed)
		return rec
	}
	rec.Output = raw

	reply, parsed := judge.Parse(raw)
	if j.clean {
		rec.Outcome = judge.JudgeClean(j.vector, reply, parsed)
		return rec
	}
	if !parsed {
		rec.Outcome = string(judge.Malformed)
		return rec
	}
	outcome, verdict := judge.Apply(j.vector, j.payload, reply, clean, j.level)
	rec.Outcome = string(outcome)
	rec.GuardWhy = verdict.Reasons
	return rec
}

func printDialog(w io.Writer, rec report.Record, systems map[string]int) {
	no, seen := systems[rec.SentSystem]
	if !seen && rec.SentSystem != "" {
		no = len(systems) + 1
		systems[rec.SentSystem] = no
	}
	mark := "не сработала"
	if rec.Kind == report.KindClean {
		mark = "чистый носитель"
	} else if judge.Outcome(rec.Outcome).Success() {
		mark = "АТАКА ПРОШЛА"
	}

	fmt.Fprintf(w, "\n%s\n", strings.Repeat("─", 72))
	fmt.Fprintf(w, "[%s / %s] %s", rec.Vector, rec.Level, rec.Payload)
	if rec.Method != "" {
		fmt.Fprintf(w, " · %s · %s", rec.Class, rec.Method)
	}
	fmt.Fprintf(w, " · повтор %d\n", rec.Repeat)
	fmt.Fprintf(w, "исход: %s — %s\n", rec.Outcome, mark)
	if len(rec.Sanitized) > 0 {
		fmt.Fprintf(w, "очистка вырезала: %v\n", rec.Sanitized)
	}
	if len(rec.GuardWhy) > 0 {
		fmt.Fprintf(w, "проверка выхода: %s\n", strings.Join(rec.GuardWhy, "; "))
	}
	if rec.Err != "" {
		fmt.Fprintf(w, "ошибка вызова: %s\n", rec.Err)
		return
	}
	if seen {
		fmt.Fprintf(w, "системный промпт: №%d, приведён выше\n", no)
	} else {
		fmt.Fprintf(w, "\n── системный промпт №%d ──\n%s\n", no, rec.SentSystem)
	}
	fmt.Fprintf(w, "\n── что ушло в модель ──\n%s\n", rec.SentUser)
	fmt.Fprintf(w, "\n── ответ модели ──\n%s\n", rec.Output)
}

func filterPayloads(all []scenario.Payload, ids []string) []scenario.Payload {
	want := map[string]bool{}
	for _, id := range ids {
		want[strings.TrimSpace(id)] = true
	}
	var out []scenario.Payload
	for _, p := range all {
		if want[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

func parseVectors(s string) ([]scenario.Vector, error) {
	var out []scenario.Vector
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, ok := scenario.ParseVector(part)
		if !ok {
			return nil, fmt.Errorf("неизвестный вектор %q", part)
		}
		out = append(out, v)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("не выбрано ни одного вектора")
	}
	return out, nil
}

func parseLevels(s string) ([]shield.Level, error) {
	var out []shield.Level
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		l, ok := shield.ParseLevel(part)
		if !ok {
			return nil, fmt.Errorf("неизвестный слой %q", part)
		}
		out = append(out, l)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("не выбрано ни одного слоя")
	}
	return out, nil
}

func parseMethods(s string) ([]hide.Method, error) {
	var out []hide.Method
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		m, ok := hide.Parse(part)
		if !ok {
			return nil, fmt.Errorf("неизвестный способ сокрытия %q", part)
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("не выбрано ни одного способа")
	}
	return out, nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
	fmt.Fprintln(os.Stderr)
	os.Exit(1)
}
