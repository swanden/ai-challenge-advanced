// Команда pipeline прогоняет одни и те же входы двумя способами — одним
// запросом и тремя этапами — и сводит результаты в таблицу.
//
// Входы те же, что в Днях 7 и 8: шестнадцать примеров из eval Дня 6 с
// известными верными ответами и шестнадцать проб, пограничных и шумных.
// Набор не меняется намеренно: только так цифры четырёх дней сравнимы
// напрямую.
//
// Запуск из корня репозитория:
//
//	go run ./week-2/task-9/cmd/pipeline -mode both
//	go run ./week-2/task-9/cmd/pipeline -mode multistage -normalize-model qwen2.5:3b
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

	"github.com/swanden/ai-challenge-advanced/week-2/task-9/internal/llm"
	"github.com/swanden/ai-challenge-advanced/week-2/task-9/internal/spec"
	"github.com/swanden/ai-challenge-advanced/week-2/task-9/internal/stages"
)

type probe struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Class string `json:"class,omitempty"`
	User  string `json:"user"`
}

type testCase struct {
	ID    string
	Group string
	User  string
	Class string
}

type record struct {
	ID      string        `json:"id"`
	Group   string        `json:"group"`
	Known   string        `json:"known_class,omitempty"`
	Result  stages.Result `json:"result"`
	Correct bool          `json:"correct"`
}

type runReport struct {
	Mode      string         `json:"mode"`
	Models    stages.Models  `json:"models"`
	StartedAt time.Time      `json:"started_at"`
	Records   []record       `json:"records"`
	Totals    map[string]any `json:"totals"`
}

type report struct {
	BaseURL   string      `json:"base_url"`
	StartedAt time.Time   `json:"started_at"`
	System    string      `json:"system_prompt"`
	Note      string      `json:"note,omitempty"`
	Runs      []runReport `json:"runs"`
}

func main() {
	evalPath := flag.String("eval", "week-2/task-6/dataset/eval.jsonl", "eval Дня 6")
	probesPath := flag.String("probes", "week-2/task-7/dataset/probes.jsonl", "пробы Дня 7")
	baseURL := flag.String("base-url", "http://localhost:11434/v1", "OpenAI-совместимый эндпоинт")
	keyEnv := flag.String("key-env", "", "переменная окружения с ключом")
	mode := flag.String("mode", "both", "режим: monolith, multistage, twostage, both или all")
	model := flag.String("model", "qwen2.5:7b", "модель по умолчанию для всех этапов")
	normalizeModel := flag.String("normalize-model", "", "модель первого этапа, если отличается")
	decideModel := flag.String("decide-model", "", "модель второго этапа, если отличается")
	outDir := flag.String("out", "week-2/task-9/evidence", "куда положить отчёт")
	note := flag.String("note", "", "пометка в отчёт")
	warmup := flag.Bool("warmup", true, "прогреть модели до замера")
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

	models := stages.Models{
		Monolith:  *model,
		Normalize: orDefault(*normalizeModel, *model),
		Decide:    orDefault(*decideModel, *model),
		Assemble:  "—", // третий этап модель не вызывает
	}
	cl := stages.Clients{
		Monolith:  llm.New(*baseURL, key, models.Monolith),
		Normalize: llm.New(*baseURL, key, models.Normalize),
		Decide:    llm.New(*baseURL, key, models.Decide),
	}

	// Прогрев: без него загрузка весов в память ложится на тот режим,
	// который шёл первым, и сравнение задержек теряет смысл.
	if *warmup {
		fmt.Print("прогрев моделей… ")
		seen := map[string]bool{}
		for _, c := range []*llm.Client{cl.Monolith, cl.Normalize, cl.Decide} {
			if seen[c.Model] {
				continue
			}
			seen[c.Model] = true
			if _, err := c.Ask(llm.Request{
				Messages:  []llm.Message{{Role: "user", Content: "ok"}},
				MaxTokens: 1,
			}); err != nil {
				fail("прогрев %s: %v", c.Model, err)
			}
		}
		fmt.Println("готово")
	}

	modes := []string{*mode}
	switch *mode {
	case "both":
		modes = []string{"monolith", "multistage"}
	case "all":
		modes = []string{"monolith", "multistage", "twostage"}
	}

	rep := report{BaseURL: *baseURL, StartedAt: time.Now(), System: sp.SystemPrompt, Note: *note}
	fmt.Printf("контракт из %s: %d классов\n", sp.Source, len(sp.Classes))
	fmt.Printf("модели: монолит %s, нормализация %s, решение %s\n\n",
		models.Monolith, models.Normalize, models.Decide)

	for _, m := range modes {
		rr, err := run(cl, sp, models, m, cases)
		rep.Runs = append(rep.Runs, rr)
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

func run(cl stages.Clients, sp *spec.Spec, models stages.Models, mode string, cases []testCase) (runReport, error) {
	rr := runReport{Mode: mode, Models: models, StartedAt: time.Now()}
	fmt.Printf("══ %s ══\n", mode)

	group := ""
	for _, tc := range cases {
		if tc.Group != group {
			group = tc.Group
			fmt.Printf("── %s ──\n", group)
		}

		var res stages.Result
		var err error
		switch mode {
		case "monolith":
			res, err = stages.Monolith(cl.Monolith, sp, tc.User)
		case "twostage":
			res, err = stages.MultiStage(cl, sp, tc.User, false)
		default:
			res, err = stages.MultiStage(cl, sp, tc.User, true)
		}

		rec := record{ID: tc.ID, Group: tc.Group, Known: tc.Class, Result: res}
		if tc.Class != "" {
			rec.Correct = res.Class == tc.Class
		}
		rr.Records = append(rr.Records, rec)
		if err != nil {
			return rr, err
		}
		printRecord(rec)
	}

	summarize(&rr)
	printTotals(rr)
	fmt.Println()
	return rr, nil
}

func printRecord(r record) {
	mark := " "
	if r.Known != "" {
		mark = "×"
		if r.Correct {
			mark = "✓"
		}
	}
	extra := ""
	if r.Result.ContractQ != "" {
		extra = " контракт=" + r.Result.ContractQ
	}
	if f := r.Result.Features; f != nil && !f.Parsed {
		extra += " признаки=?"
	}
	if !r.Result.Consistent {
		extra += " ⚠ " + r.Result.Conflict
	}
	fmt.Printf("%s %-12s %-16s %d выз.%s\n", mark, r.ID, orDash(r.Result.Class), r.Result.CallCount, extra)
}

func summarize(rr *runReport) {
	t := map[string]any{}
	var calls, prompt, completion int
	var latency int64
	known, correct := 0, 0
	inconsistent, unparsed := 0, 0
	contractYes := 0
	byClass := map[string]map[string]int{}

	for _, r := range rr.Records {
		calls += r.Result.CallCount
		prompt += r.Result.PromptTokens
		completion += r.Result.CompletionTokens
		latency += r.Result.LatencyMS
		if !r.Result.Consistent {
			inconsistent++
		}
		if f := r.Result.Features; f != nil && !f.Parsed {
			unparsed++
		}
		if r.Result.ContractQ == "yes" {
			contractYes++
		}
		if r.Known != "" {
			known++
			if r.Correct {
				correct++
			}
			c := byClass[r.Known]
			if c == nil {
				c = map[string]int{}
				byClass[r.Known] = c
			}
			c["всего"]++
			if r.Correct {
				c["верно"]++
			}
		}
	}

	t["входов"] = len(rr.Records)
	t["вызовов"] = calls
	if len(rr.Records) > 0 {
		t["вызовов на вход"] = float64(calls) / float64(len(rr.Records))
		t["задержка на вход мс"] = latency / int64(len(rr.Records))
	}
	t["токенов промпта"] = prompt
	t["токенов ответа"] = completion
	t["задержка суммарно мс"] = latency
	t["ответ yes на вопрос о контракте"] = contractYes
	t["несогласованных"] = inconsistent
	t["признаки не разобраны"] = unparsed
	if known > 0 {
		t["входов с известным ответом"] = known
		t["верных"] = correct
		t["точность"] = float64(correct) / float64(known)
		t["по классам"] = byClass
	}
	rr.Totals = t
}

func printTotals(rr runReport) {
	t := rr.Totals
	fmt.Printf("\nвходов %v, вызовов %v (%.2f на вход)\n", t["входов"], t["вызовов"], t["вызовов на вход"])
	fmt.Printf("токенов: промпт %v, ответ %v\n", t["токенов промпта"], t["токенов ответа"])
	fmt.Printf("задержка %v мс, в среднем %v мс на вход\n", t["задержка суммарно мс"], t["задержка на вход мс"])
	if v, ok := t["точность"]; ok {
		fmt.Printf("точность %.0f%% (%v из %v)\n", v.(float64)*100, t["верных"], t["входов с известным ответом"])
	}
	if rr.Mode != "monolith" {
		fmt.Printf("вопрос о контракте: yes на %v входах\n", t["ответ yes на вопрос о контракте"])
		fmt.Printf("признаки не разобраны: %v, несогласованных результатов: %v\n",
			t["признаки не разобраны"], t["несогласованных"])
	}
	if raw, ok := t["по классам"]; ok {
		byClass, _ := raw.(map[string]map[string]int)
		keys := make([]string, 0, len(byClass))
		for k := range byClass {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s %d/%d", k, byClass[k]["верно"], byClass[k]["всего"]))
		}
		fmt.Printf("по классам: %s\n", strings.Join(parts, ", "))
	}
}

func compare(rep report) {
	if len(rep.Runs) < 2 {
		return
	}
	fmt.Printf("\n══ сравнение ══\n")
	fmt.Printf("%-12s %10s %12s %14s %14s\n", "вариант", "точность", "вызовов", "токенов", "задержка/вход")
	for _, r := range rep.Runs {
		acc := "—"
		if v, ok := r.Totals["точность"]; ok {
			acc = fmt.Sprintf("%.0f%%", v.(float64)*100)
		}
		fmt.Printf("%-12s %10s %12v %14v %14v\n",
			r.Mode, acc, r.Totals["вызовов"], r.Totals["токенов промпта"], r.Totals["задержка на вход мс"])
	}
}

func load(sp *spec.Spec, examples []spec.Example, probesPath string) ([]testCase, error) {
	var cases []testCase
	for i, ex := range examples {
		cases = append(cases, testCase{
			ID: fmt.Sprintf("eval-%02d", i+1), Group: "correct",
			User: ex.User(), Class: ex.Class(),
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
	return append(append(cases, borderline...), noisy...), nil
}

func save(rep report, outDir string) string {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "pipeline: отчёт не сохранён: %v\n", err)
		return ""
	}
	name := fmt.Sprintf("pipeline-%s.json", rep.StartedAt.Format("20060102-150405"))
	path := filepath.Join(outDir, name)
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "pipeline: отчёт не сохранён: %v\n", err)
		return ""
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "pipeline: отчёт не сохранён: %v\n", err)
		return ""
	}
	return path
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "pipeline: "+format+"\n", args...)
	os.Exit(1)
}
