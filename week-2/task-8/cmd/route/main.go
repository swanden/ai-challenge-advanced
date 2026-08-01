// Команда route прогоняет входы через маршрутизатор и сравнивает три режима:
// всё на малой модели, маршрутизация с эскалацией, всё на большой.
//
// Три режима нужны потому, что маршрутизация сама по себе ничего не
// доказывает. Смысл у неё появляется только между двумя крайностями:
// насколько она приблизилась к качеству большой модели и какую долю её
// стоимости при этом заплатила.
//
// Входы те же, что в Дне 7: шестнадцать примеров из eval Дня 6, у которых
// известен верный класс, и шестнадцать проб — пограничных и шумных.
//
// Запуск из корня репозитория:
//
//	go run ./week-2/task-8/cmd/route -mode all
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-2/task-8/internal/llm"
	"github.com/swanden/ai-challenge-advanced/week-2/task-8/internal/router"
	"github.com/swanden/ai-challenge-advanced/week-2/task-8/internal/spec"
)

type probe struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Expect string `json:"expect"`
	Class  string `json:"class,omitempty"`
	User   string `json:"user"`
	Why    string `json:"why"`
}

type testCase struct {
	ID    string
	Group string
	User  string
	Class string // известный верный класс, если он есть
}

type record struct {
	ID       string          `json:"id"`
	Group    string          `json:"group"`
	Class    string          `json:"known_class,omitempty"`
	Decision router.Decision `json:"decision"`
	Correct  bool            `json:"correct"`
	Rescued  bool            `json:"rescued"`  // малая ошиблась, большая исправила
	Spoiled  bool            `json:"spoiled"`  // малая была права, большая испортила
	Wasted   bool            `json:"wasted"`   // эскалация была не нужна
}

type modeReport struct {
	Mode      string         `json:"mode"`
	Records   []record       `json:"records"`
	Totals    map[string]any `json:"totals"`
	StartedAt time.Time      `json:"started_at"`
}

type report struct {
	SmallModel string       `json:"small_model"`
	BigModel   string       `json:"big_model"`
	BaseURL    string       `json:"base_url"`
	StartedAt  time.Time    `json:"started_at"`
	System     string       `json:"system_prompt"`
	Policy     router.Policy `json:"policy"`
	Note       string       `json:"note,omitempty"`
	Runs       []modeReport `json:"runs"`
}

func main() {
	evalPath := flag.String("eval", "week-2/task-6/dataset/eval.jsonl", "eval Дня 6")
	probesPath := flag.String("probes", "week-2/task-7/dataset/probes.jsonl", "пробы Дня 7")
	smallModel := flag.String("small", "qwen2.5:7b", "модель первой ступени")
	bigModel := flag.String("big", "qwen2.5:14b", "модель второй ступени")
	baseURL := flag.String("base-url", "http://localhost:11434/v1", "OpenAI-совместимый эндпоинт")
	keyEnv := flag.String("key-env", "", "переменная окружения с ключом; пустая — без авторизации")
	mode := flag.String("mode", "all", "режим: small, route, big или all")
	outDir := flag.String("out", "week-2/task-8/evidence", "куда положить отчёт")
	note := flag.String("note", "", "пометка в отчёт")
	minSeqProb := flag.Float64("min-seq-prob", router.Default().MinSeqProb, "порог вероятности")
	minMargin := flag.Float64("min-margin", router.Default().MinMargin, "порог отрыва")
	maxAnswerTokens := flag.Int("max-answer-tokens", router.Default().MaxAnswerTokens, "длина ответа, выше которой эскалируем")
	useMarkers := flag.Bool("markers", true, "включать разбор формулировки на признаки контракта")
	warmup := flag.Bool("warmup", true, "прогреть обе модели до замера")
	flag.Parse()

	sp, examples, err := spec.Load(*evalPath)
	if err != nil {
		fail("%v", err)
	}
	cases, err := load(sp, examples, *probesPath)
	if err != nil {
		fail("%v", err)
	}

	var key string
	if *keyEnv != "" {
		key = os.Getenv(*keyEnv)
		if key == "" {
			fail("переменная %s пуста", *keyEnv)
		}
	}
	small := llm.New(*baseURL, key, *smallModel)
	big := llm.New(*baseURL, key, *bigModel)

	modes := []string{*mode}
	if *mode == "all" {
		modes = []string{router.ModeSmall, router.ModeRoute, router.ModeBig}
	}
	for _, m := range modes {
		if m != router.ModeSmall && m != router.ModeRoute && m != router.ModeBig {
			fail("неизвестный режим %q", m)
		}
	}

	rep := report{
		SmallModel: *smallModel,
		BigModel:   *bigModel,
		BaseURL:    *baseURL,
		StartedAt:  time.Now(),
		System:     sp.SystemPrompt,
		Note:       *note,
	}

	// Прогрев. Без него первый вызов каждой модели включает загрузку весов
	// в память, и сравнение задержек между режимами становится бессмысленным:
	// режим, отработавший первым, платит за загрузку, остальные — нет.
	if *warmup {
		fmt.Print("прогрев моделей… ")
		for _, c := range []*llm.Client{small, big} {
			if _, err := c.Ask(llm.Request{
				Messages:  []llm.Message{{Role: "user", Content: "ok"}},
				MaxTokens: 1,
			}); err != nil {
				fail("прогрев %s: %v", c.Model, err)
			}
		}
		fmt.Println("готово")
	}

	fmt.Printf("контракт из %s: %d классов\n", sp.Source, len(sp.Classes))
	fmt.Printf("малая %s, большая %s\n", *smallModel, *bigModel)
	fmt.Printf("пороги: вероятность %.2f, отрыв %.2f, длина ответа %d, разбор формулировки %v\n\n",
		*minSeqProb, *minMargin, *maxAnswerTokens, *useMarkers)

	for _, m := range modes {
		pol := router.Policy{
			Mode:            m,
			MinSeqProb:      *minSeqProb,
			MinMargin:       *minMargin,
			MaxAnswerTokens: *maxAnswerTokens,
			MinInputLen:     router.Default().MinInputLen,
			UseMarkers:      *useMarkers,
		}
		rep.Policy = pol
		mr, err := run(small, big, sp, pol, cases)
		rep.Runs = append(rep.Runs, mr)
		if err != nil {
			save(rep, *outDir)
			fail("режим %s: %v", m, err)
		}
	}

	compare(rep)
	if path := save(rep, *outDir); path != "" {
		fmt.Printf("\nотчёт: %s\n", path)
	}
}

func run(small, big *llm.Client, sp *spec.Spec, pol router.Policy, cases []testCase) (modeReport, error) {
	mr := modeReport{Mode: pol.Mode, StartedAt: time.Now()}
	fmt.Printf("══ режим %s ══\n", pol.Mode)

	group := ""
	for _, tc := range cases {
		if tc.Group != group {
			group = tc.Group
			fmt.Printf("── %s ──\n", group)
		}
		d, err := router.Route(small, big, sp, pol, tc.User)
		rec := record{ID: tc.ID, Group: tc.Group, Class: tc.Class, Decision: d}
		if tc.Class != "" {
			rec.Correct = d.Final == tc.Class
			if d.Escalated && d.Small != nil {
				smallCorrect := d.Small.Class == tc.Class
				rec.Rescued = !smallCorrect && rec.Correct
				rec.Spoiled = smallCorrect && !rec.Correct
				rec.Wasted = smallCorrect && rec.Correct
			}
		}
		mr.Records = append(mr.Records, rec)
		if err != nil {
			return mr, err
		}
		printRecord(rec)
	}
	summarize(&mr)
	printTotals(mr)
	fmt.Println()
	return mr, nil
}

func printRecord(r record) {
	mark := " "
	if r.Class != "" {
		mark = "×"
		if r.Correct {
			mark = "✓"
		}
	}
	where := r.Decision.DecidedBy
	if r.Decision.Escalated {
		where = "small→big"
	}
	extra := ""
	if len(r.Decision.Triggers) > 0 {
		extra = " ← " + strings.Join(r.Decision.Triggers, ", ")
	}
	fmt.Printf("%s %-12s %-10s %-16s%s\n", mark, r.ID, where, orDash(r.Decision.Final), extra)
}

func summarize(mr *modeReport) {
	t := map[string]any{}
	var calls, bigCalls, prompt, completion int
	var latency int64
	escalated, staySmall, rejected := 0, 0, 0
	byTrigger := map[string]int{}

	known, correct := 0, 0
	rescued, spoiled, wasted := 0, 0, 0
	byClass := map[string]map[string]int{}

	for _, r := range mr.Records {
		d := r.Decision
		calls += d.Calls
		bigCalls += d.BigCalls
		prompt += d.PromptTokens
		completion += d.CompletionTokens
		latency += d.LatencyMS
		switch {
		case !d.InputOK:
			rejected++
		case d.Escalated:
			escalated++
		default:
			staySmall++
		}
		for _, tr := range d.Triggers {
			// Порог в тексте признака мешает считать, поэтому берём
			// только его название до первого пробела с цифрой.
			byTrigger[triggerName(tr)]++
		}
		if r.Rescued {
			rescued++
		}
		if r.Spoiled {
			spoiled++
		}
		if r.Wasted {
			wasted++
		}
		if r.Class != "" {
			known++
			if r.Correct {
				correct++
			}
			c := byClass[r.Class]
			if c == nil {
				c = map[string]int{}
				byClass[r.Class] = c
			}
			c["всего"]++
			if r.Correct {
				c["верно"]++
			}
		}
	}

	t["входов"] = len(mr.Records)
	t["осталось на малой"] = staySmall
	t["ушло на большую"] = escalated
	t["отвергнуто до инференса"] = rejected
	t["вызовов всего"] = calls
	t["вызовов большой модели"] = bigCalls
	t["токенов промпта"] = prompt
	t["токенов ответа"] = completion
	t["задержка суммарно мс"] = latency
	if len(mr.Records) > 0 {
		t["задержка на вход мс"] = latency / int64(len(mr.Records))
	}
	t["по признакам эскалации"] = byTrigger
	if known > 0 {
		t["входов с известным ответом"] = known
		t["верных"] = correct
		t["точность"] = float64(correct) / float64(known)
		t["эскалация исправила ошибку"] = rescued
		t["эскалация испортила верный ответ"] = spoiled
		t["эскалация была не нужна"] = wasted
		t["по классам"] = byClass
	}
	mr.Totals = t
}

// triggerName убирает из признака числовое значение, чтобы одинаковые
// признаки складывались в одну корзину.
func triggerName(s string) string {
	for _, prefix := range []string{"вероятность", "отрыв", "длина ответа"} {
		if strings.HasPrefix(s, prefix) {
			return prefix
		}
	}
	return s
}

func printTotals(mr modeReport) {
	t := mr.Totals
	fmt.Printf("\nвходов %v: на малой %v, на большой %v, отвергнуто %v\n",
		t["входов"], t["осталось на малой"], t["ушло на большую"], t["отвергнуто до инференса"])
	fmt.Printf("вызовов %v, из них большой модели %v\n", t["вызовов всего"], t["вызовов большой модели"])
	fmt.Printf("задержка %v мс, в среднем %v мс на вход\n",
		t["задержка суммарно мс"], t["задержка на вход мс"])
	if v, ok := t["точность"]; ok {
		fmt.Printf("точность %.0f%% (%v из %v)\n", v.(float64)*100, t["верных"], t["входов с известным ответом"])
	}
	if raw, ok := t["по признакам эскалации"]; ok {
		byTrigger, _ := raw.(map[string]int)
		if len(byTrigger) > 0 {
			keys := make([]string, 0, len(byTrigger))
			for k := range byTrigger {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			parts := make([]string, 0, len(keys))
			for _, k := range keys {
				parts = append(parts, fmt.Sprintf("%s %d", k, byTrigger[k]))
			}
			fmt.Printf("признаки эскалации: %s\n", strings.Join(parts, ", "))
		}
	}
	if v, ok := t["эскалация исправила ошибку"]; ok {
		fmt.Printf("эскалация: исправила %v, испортила %v, была не нужна %v\n",
			v, t["эскалация испортила верный ответ"], t["эскалация была не нужна"])
	}
}

// compare сводит режимы в одну таблицу — ради неё всё и затевалось.
func compare(rep report) {
	if len(rep.Runs) < 2 {
		return
	}
	fmt.Printf("\n══ сравнение режимов ══\n")
	fmt.Printf("%-8s %10s %14s %12s %14s\n", "режим", "точность", "вызовов", "из них big", "задержка/вход")
	for _, r := range rep.Runs {
		acc := "—"
		if v, ok := r.Totals["точность"]; ok {
			acc = fmt.Sprintf("%.0f%%", v.(float64)*100)
		}
		fmt.Printf("%-8s %10s %14v %12v %14v\n",
			r.Mode, acc, r.Totals["вызовов всего"], r.Totals["вызовов большой модели"],
			r.Totals["задержка на вход мс"])
	}
}

func load(sp *spec.Spec, examples []spec.Example, probesPath string) ([]testCase, error) {
	var cases []testCase
	for i, ex := range examples {
		cases = append(cases, testCase{
			ID:    fmt.Sprintf("eval-%02d", i+1),
			Group: "correct",
			User:  ex.User(),
			Class: ex.Class(),
		})
	}
	lines, err := spec.ReadLines(probesPath)
	if err != nil {
		return nil, err
	}
	var borderline, noisy []testCase
	for _, ln := range lines {
		var p probe
		if err := json.Unmarshal([]byte(ln.Raw), &p); err != nil {
			return nil, fmt.Errorf("%s строка %d: %w", probesPath, ln.No, err)
		}
		if p.Class != "" && !sp.IsClass(p.Class) {
			return nil, fmt.Errorf("%s: у %s класс %q вне контракта", probesPath, p.ID, p.Class)
		}
		tc := testCase{ID: p.ID, Group: p.Kind, User: p.User, Class: p.Class}
		switch p.Kind {
		case "borderline":
			borderline = append(borderline, tc)
		case "noisy":
			noisy = append(noisy, tc)
		default:
			return nil, fmt.Errorf("%s: у %s неизвестная группа %q", probesPath, p.ID, p.Kind)
		}
	}
	cases = append(cases, borderline...)
	cases = append(cases, noisy...)
	return cases, nil
}

func save(rep report, outDir string) string {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "route: отчёт не сохранён: %v\n", err)
		return ""
	}
	name := fmt.Sprintf("routing-%s-%s-%s.json",
		clean(rep.SmallModel), clean(rep.BigModel), rep.StartedAt.Format("20060102-150405"))
	path := filepath.Join(outDir, name)
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "route: отчёт не сохранён: %v\n", err)
		return ""
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "route: отчёт не сохранён: %v\n", err)
		return ""
	}
	return path
}

func clean(s string) string {
	return strings.NewReplacer("/", "_", ":", "_").Replace(s)
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "route: "+format+"\n", args...)
	os.Exit(1)
}
