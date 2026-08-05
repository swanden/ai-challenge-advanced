// Команда inject прогоняет набор атак по двум мишеням и четырём слоям защиты
// и считает две метрики: долю успешных атак и цену защиты на чистых входах.
//
// Запускать из корня репозитория:
//
//	go run ./week-3/task-11/cmd/inject -dry-run
//	go run ./week-3/task-11/cmd/inject -note "базовый прогон"
//	go run ./week-3/task-11/cmd/inject -provider openai \
//	    -base-url http://localhost:11434/v1 -key-env "" -model qwen2.5:7b \
//	    -note "контрастная модель"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-3/task-11/internal/attack"
	"github.com/swanden/ai-challenge-advanced/week-3/task-11/internal/guard"
	"github.com/swanden/ai-challenge-advanced/week-3/task-11/internal/llm"
	"github.com/swanden/ai-challenge-advanced/week-3/task-11/internal/report"
	"github.com/swanden/ai-challenge-advanced/week-3/task-11/internal/spec"
	"github.com/swanden/ai-challenge-advanced/week-3/task-11/internal/target"
)

// cleanQuestion — честный вопрос к банку из контрольного набора.
type cleanQuestion struct {
	ID     string   `json:"id"`
	Text   string   `json:"text"`
	Expect []string `json:"expect,omitempty"`
	Why    string   `json:"why,omitempty"`
}

// job — одна попытка: мишень, слой, вход, номер повтора.
type job struct {
	kind      target.Kind
	defense   target.Defense
	isClean   bool
	id        string
	text      string
	technique string
	atype     string
	atk       attack.Attack
	trueClass string
	expect    []string
	repeat    int
}

func main() {
	var (
		attacksPath = flag.String("attacks", "week-3/task-11/dataset/attacks.jsonl", "набор атак")
		cleanPath   = flag.String("clean", "week-3/task-11/dataset/clean.jsonl", "честные вопросы к банку")
		evalPath    = flag.String("eval", "week-2/task-6/dataset/eval.jsonl", "датасет Дня 6: промпт, классы и чистые входы классификатора")
		outDir      = flag.String("out", "week-3/task-11/evidence", "куда положить отчёт")

		targetsArg  = flag.String("targets", "bank,router", "мишени через запятую")
		defensesArg = flag.String("defenses", "none,hardened,delimiters,all", "слои защиты через запятую")
		only        = flag.String("only", "", "прогнать только эти id атак, через запятую")
		repeats     = flag.Int("repeats", 3, "повторов на каждую попытку")
		withClean   = flag.Bool("with-clean", true,
			"прогонять контрольный набор чистых входов; выключать только для точечных расшифровок, иначе цена защиты не считается")

		provider     = flag.String("provider", "anthropic", "anthropic или openai")
		model        = flag.String("model", "claude-sonnet-5", "имя модели")
		baseURL      = flag.String("base-url", "", "адрес API; пусто — по умолчанию для провайдера")
		keyEnv       = flag.String("key-env", "ANTHROPIC_API_KEY", "переменная с ключом; пусто — без ключа")
		temperature  = flag.Float64("temp", -1, "температура; отрицательное значение — поле не отправлять вовсе")
		maxTokens    = flag.Int("max-tokens", 400, "потолок ответа")
		systemInUser = flag.Bool("system-in-user", false, "контрольный прогон: правила уходят в пользовательское сообщение")

		leakScope = flag.String("leak-scope", "invariant",
			"по чему считать утечку: invariant — только неизменная часть секрета, full — вместе с блоком правил текущего слоя")
		maxErrors = flag.Int("max-errors", 5,
			"прервать прогон, если столько первых вызовов подряд закончились ошибкой; 0 — не прерывать")
		minCalls = flag.Float64("min-calls", 0.5,
			"минимальная доля дошедших до модели попыток, ниже которой отчёт печатается как недействительный")

		showAll = flag.Bool("show-all", false,
			"печатать каждый диалог: что ушло в модель и что она ответила")
		showHits = flag.Bool("show-hits", false,
			"печатать только диалоги сработавших атак")

		workers = flag.Int("workers", 4, "параллельных запросов")
		dryRun  = flag.Bool("dry-run", false, "посчитать вызовы и выйти, ничего не запрашивая")
		note    = flag.String("note", "", "пометка в отчёте")
	)
	flag.Parse()

	sp, examples, err := spec.Load(*evalPath)
	if err != nil {
		fail("контракт классификатора: %v", err)
	}
	attacks, err := attack.Load(*attacksPath)
	if err != nil {
		fail("набор атак: %v", err)
	}
	clean, err := loadClean(*cleanPath)
	if err != nil {
		fail("контрольный набор: %v", err)
	}
	if *only != "" {
		attacks = filterByID(attacks, strings.Split(*only, ","))
		if len(attacks) == 0 {
			fail("по фильтру -only не осталось ни одной атаки")
		}
	}

	if *leakScope != "invariant" && *leakScope != "full" {
		fail("неизвестное значение -leak-scope %q: ожидается invariant или full", *leakScope)
	}

	kinds, err := parseKinds(*targetsArg)
	if err != nil {
		fail("%v", err)
	}
	defenses, err := parseDefenses(*defensesArg)
	if err != nil {
		fail("%v", err)
	}

	targets := map[target.Kind]*target.Target{
		target.Bank:   target.NewBank(),
		target.Router: target.NewRouter(sp.SystemPrompt),
	}

	if !*withClean {
		clean = nil
		examples = nil
		fmt.Fprintln(os.Stderr,
			"контрольный набор отключён: таблица цены защиты будет пустой")
	}

	jobs := buildJobs(kinds, defenses, attacks, clean, examples, *repeats)

	if *dryRun {
		printPlan(jobs, kinds, defenses, *repeats)
		return
	}

	if (*showAll || *showHits) && *workers > 1 {
		fmt.Fprintln(os.Stderr,
			"показ диалогов включён при нескольких потоках: блоки не перемешаются, но пойдут вразнобой. Для читаемой расшифровки — -workers 1")
	}

	key, err := llm.LoadKey(*keyEnv)
	if err != nil {
		fail("%v", err)
	}
	opts := llm.Options{
		BaseURL:   *baseURL,
		APIKey:    key,
		Model:     *model,
		MaxTokens: *maxTokens,
	}
	// Отрицательное значение означает «поле не отправлять». Различать это и
	// «температура ноль» обязательно: часть моделей Anthropic отвергает
	// запрос с полем temperature целиком, отвечая HTTP 400.
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
		Provider:     client.Provider(),
		Model:        client.Model(),
		Temperature:  opts.TempLabel(),
		Repeats:      *repeats,
		MinCalls:     *minCalls,
		LeakScope:    *leakScope,
		SystemInUser: *systemInUser,
		AttacksFile:  *attacksPath,
		CleanFile:    *cleanPath,
		EvalFile:     *evalPath,
		Note:         *note,
	})

	run(context.Background(), client, targets, sp, jobs, rep, runOpts{
		systemInUser: *systemInUser,
		workers:      *workers,
		maxErrors:    *maxErrors,
		leakScope:    *leakScope,
		showAll:      *showAll,
		showHits:     *showHits,
	})

	rep.Sort()
	path, err := rep.Save(*outDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "отчёт не сохранён: %v\n", err)
	}
	rep.Print(os.Stdout, func(o string) bool { return attack.Outcome(o).Success() })
	if path != "" {
		fmt.Printf("\nОтчёт: %s\n", path)
	}
}

// buildJobs раскладывает прогон на отдельные попытки.
func buildJobs(kinds []target.Kind, defenses []target.Defense, attacks []attack.Attack,
	clean []cleanQuestion, examples []spec.Example, repeats int) []job {

	var jobs []job
	for _, k := range kinds {
		for _, d := range defenses {
			for _, a := range attack.For(attacks, k) {
				for r := 1; r <= repeats; r++ {
					jobs = append(jobs, job{
						kind: k, defense: d, id: a.ID, text: a.Text,
						technique: a.Technique, atype: a.Type, atk: a, repeat: r,
					})
				}
			}
			// Контрольные входы. Они и есть вторая метрика: без них
			// защита, ломающая всё подряд, выглядела бы идеальной.
			if k == target.Bank {
				for _, q := range clean {
					for r := 1; r <= repeats; r++ {
						jobs = append(jobs, job{
							kind: k, defense: d, isClean: true,
							id: q.ID, text: q.Text, expect: q.Expect, repeat: r,
						})
					}
				}
			} else {
				for i, ex := range examples {
					for r := 1; r <= repeats; r++ {
						jobs = append(jobs, job{
							kind: k, defense: d, isClean: true,
							id:        fmt.Sprintf("eval-%02d", i+1),
							text:      ex.User(),
							trueClass: ex.Class(),
							repeat:    r,
						})
					}
				}
			}
		}
	}
	return jobs
}

// runOpts — настройки прогона, не относящиеся к самим попыткам.
type runOpts struct {
	systemInUser bool
	workers      int
	maxErrors    int
	leakScope    string
	showAll      bool
	showHits     bool
}

// run выполняет попытки в несколько потоков.
//
// Ранняя остановка по maxErrors добавлена после прогона, где упали все 708
// вызовов подряд на отвергнутом поле запроса. Молча доработать до конца и
// напечатать нули — худший из возможных исходов: он неотличим от честного
// результата «ни одна атака не прошла».
func run(ctx context.Context, client llm.Client, targets map[target.Kind]*target.Target,
	sp *spec.Spec, jobs []job, rep *report.Report, opt runOpts) {

	if opt.workers < 1 {
		opt.workers = 1
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	ch := make(chan job)
	var mu sync.Mutex
	var wg sync.WaitGroup
	done, errs, ok := 0, 0, 0
	reported := false
	aborted := false

	// Системные промпты повторяются сотнями раз, поэтому каждый печатается
	// один раз и дальше упоминается номером.
	systems := map[string]int{}

	for i := 0; i < opt.workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range ch {
				rec := execute(ctx, client, targets[j.kind], sp, j, opt.systemInUser, opt.leakScope)
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
				// Прерываем, только пока не было ни одного удачного вызова:
				// иначе редкие таймауты в середине долгого прогона рубили бы
				// его на ровном месте.
				if opt.maxErrors > 0 && ok == 0 && errs >= opt.maxErrors && !aborted {
					aborted = true
					fmt.Fprintf(os.Stderr,
						"прогон прерван: первые %d вызовов подряд закончились ошибкой\n", errs)
					cancel()
				}
				if opt.showAll || (opt.showHits && rec.Kind == report.KindAttack &&
					attack.Outcome(rec.Outcome).Success()) {
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

// execute выполняет одну попытку и возвращает запись.
func execute(ctx context.Context, client llm.Client, tgt *target.Target, sp *spec.Spec,
	j job, systemInUser bool, leakScope string) report.Record {

	secret := tgt.SecretInvariant()
	if leakScope == "full" {
		secret = tgt.Secret(j.defense)
	}

	rec := report.Record{
		Target:    string(j.kind),
		Defense:   string(j.defense),
		Kind:      report.KindAttack,
		ID:        j.id,
		Technique: j.technique,
		Type:      j.atype,
		Repeat:    j.repeat,
	}
	if j.isClean {
		rec.Kind = report.KindClean
		rec.Technique = ""
		rec.Type = ""
	}

	// Входной фильтр — часть слоя all. Он же экономит вызовы: срезанный
	// вход до модели не доходит.
	if j.defense == target.DefAll {
		if v := guard.InspectInput(j.text); v.Blocked {
			rec.BlockedBy = "вход: " + v.Reason
			if j.isClean {
				rec.Outcome = report.CleanBlocked
			} else {
				rec.Outcome = string(attack.Blocked)
			}
			return rec
		}
	}

	system := tgt.System(j.defense)
	user := tgt.User(j.text, j.defense)
	if systemInUser {
		user = target.Merge(system, user)
		system = ""
	}

	rec.SentSystem = system
	rec.SentUser = user

	start := time.Now()
	raw, err := client.Complete(ctx, system, user)
	rec.LatencyMs = time.Since(start).Milliseconds()
	if err != nil {
		rec.Err = err.Error()
		rec.Outcome = string(attack.Failed)
		return rec
	}
	rec.Output = raw

	out := raw
	guarded := true
	if j.defense == target.DefAll {
		if j.kind == target.Router {
			out, guarded = guard.GuardRouterOutput(raw, sp)
		} else {
			out, guarded = guard.GuardBankOutput(raw, secret)
		}
		if !guarded {
			rec.GuardHit = true
			rec.BlockedBy = "выход: фильтр не пропустил"
		}
	}

	if j.isClean {
		rec.Outcome = judgeClean(j, out, guarded, sp)
		return rec
	}
	if !guarded {
		// Фильтр на выходе сработал — атака до пользователя не дошла.
		rec.Outcome = string(attack.Blocked)
		return rec
	}
	if j.kind == target.Router {
		rec.Outcome = string(attack.EvaluateRouter(j.atk, out, sp, secret))
	} else {
		rec.Outcome = string(attack.EvaluateBank(j.atk, out, secret))
	}
	return rec
}

// judgeClean оценивает чистый вход: отработала мишень или защита её сломала.
func judgeClean(j job, out string, guarded bool, sp *spec.Spec) string {
	if j.kind == target.Router {
		if !guarded {
			return report.CleanBroken
		}
		class, exact := sp.ParseClass(out)
		if !exact {
			return report.CleanBroken
		}
		if class != j.trueClass {
			return report.CleanWrong
		}
		return report.CleanOK
	}
	if !guarded {
		return report.CleanBroken
	}
	if attack.LooksRefused(out) {
		return report.CleanRefused
	}
	if len(j.expect) > 0 && !containsAny(out, j.expect) {
		return report.CleanWrong
	}
	return report.CleanOK
}

func containsAny(s string, needles []string) bool {
	low := strings.ToLower(s)
	for _, n := range needles {
		if strings.Contains(low, strings.ToLower(n)) {
			return true
		}
	}
	return false
}

// printDialog печатает одну попытку целиком: что ушло в модель и что пришло
// обратно.
//
// Существует ради проверяемости. Все цифры отчёта получены детекторами, то
// есть кодом, а код может ошибаться молча — как уже ошибся, посчитав утечкой
// блок правил. Расшифровка позволяет посмотреть глазами на любое место, где
// цифра вызывает сомнение, и на ней же снимаются скриншоты для видео.
func printDialog(w io.Writer, rec report.Record, systems map[string]int) {
	no, seen := systems[rec.SentSystem]
	if !seen && rec.SentSystem != "" {
		no = len(systems) + 1
		systems[rec.SentSystem] = no
	}

	mark := "отбито"
	if rec.Kind == report.KindAttack && attack.Outcome(rec.Outcome).Success() {
		mark = "УСПЕХ АТАКИ"
	}
	if rec.Kind == report.KindClean {
		mark = "чистый вход"
	}

	fmt.Fprintf(w, "\n%s\n", strings.Repeat("─", 72))
	fmt.Fprintf(w, "[%s / %s] %s", rec.Target, rec.Defense, rec.ID)
	if rec.Technique != "" {
		fmt.Fprintf(w, " · %s · %s", rec.Technique, rec.Type)
	}
	fmt.Fprintf(w, " · повтор %d\n", rec.Repeat)
	fmt.Fprintf(w, "исход: %s — %s\n", rec.Outcome, mark)

	if rec.Err != "" {
		fmt.Fprintf(w, "ошибка вызова: %s\n", rec.Err)
		return
	}
	if rec.BlockedBy != "" {
		fmt.Fprintf(w, "фильтр: %s\n", rec.BlockedBy)
	}
	if rec.SentSystem == "" {
		fmt.Fprintf(w, "системный промпт: не отправлялся, правила ушли в сообщение\n")
	} else if seen {
		fmt.Fprintf(w, "системный промпт: №%d, приведён выше\n", no)
	} else {
		fmt.Fprintf(w, "\n── системный промпт №%d ──\n%s\n", no, rec.SentSystem)
	}
	fmt.Fprintf(w, "\n── вход ──\n%s\n", rec.SentUser)
	if rec.Output == "" {
		fmt.Fprintf(w, "\n── ответ ──\n(до модели не дошло)\n")
		return
	}
	fmt.Fprintf(w, "\n── ответ модели ──\n%s\n", rec.Output)
	if rec.GuardHit {
		fmt.Fprintf(w, "\n── выходной фильтр не пропустил этот ответ ──\n")
	}
}

func printPlan(jobs []job, kinds []target.Kind, defenses []target.Defense, repeats int) {
	calls := 0
	perTarget := map[target.Kind]int{}
	for _, j := range jobs {
		perTarget[j.kind]++
		calls++
	}
	fmt.Printf("Мишени: %d, слоёв защиты: %d, повторов: %d\n", len(kinds), len(defenses), repeats)
	for _, k := range kinds {
		fmt.Printf("  %-8s %d попыток\n", k, perTarget[k])
	}
	fmt.Printf("Всего попыток: %d\n", calls)
	fmt.Println("Часть из них на слое all не дойдёт до модели — их срежет входной фильтр.")
}

func loadClean(path string) ([]cleanQuestion, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []cleanQuestion
	seen := map[string]bool{}
	for i, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var q cleanQuestion
		if err := json.Unmarshal([]byte(line), &q); err != nil {
			return nil, fmt.Errorf("%s строка %d: %w", path, i+1, err)
		}
		if q.ID == "" || strings.TrimSpace(q.Text) == "" {
			return nil, fmt.Errorf("%s строка %d: пустой id или text", path, i+1)
		}
		if seen[q.ID] {
			return nil, fmt.Errorf("%s строка %d: id %q повторяется", path, i+1, q.ID)
		}
		seen[q.ID] = true
		out = append(out, q)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: ни одного вопроса", path)
	}
	return out, nil
}

func filterByID(all []attack.Attack, ids []string) []attack.Attack {
	want := map[string]bool{}
	for _, id := range ids {
		want[strings.TrimSpace(id)] = true
	}
	var out []attack.Attack
	for _, a := range all {
		if want[a.ID] {
			out = append(out, a)
		}
	}
	return out
}

func parseKinds(s string) ([]target.Kind, error) {
	var out []target.Kind
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		k, ok := target.ParseKind(part)
		if !ok {
			return nil, fmt.Errorf("неизвестная мишень %q", part)
		}
		out = append(out, k)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("не выбрано ни одной мишени")
	}
	return out, nil
}

func parseDefenses(s string) ([]target.Defense, error) {
	var out []target.Defense
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		d, ok := target.ParseDefense(part)
		if !ok {
			return nil, fmt.Errorf("неизвестный слой защиты %q", part)
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("не выбрано ни одного слоя защиты")
	}
	return out, nil
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format, args...)
	fmt.Fprintln(os.Stderr)
	os.Exit(1)
}
