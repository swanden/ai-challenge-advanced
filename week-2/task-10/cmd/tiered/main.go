// Команда tiered прогоняет запросы через двухуровневый инференс: сначала
// дешёвый классификатор, при его сомнении — языковая модель.
//
// Обучающая выборка — train.jsonl Дня 6. Она за четыре дня ни разу не
// участвовала ни в одном замере, поэтому проверка на eval честная: первый
// уровень видел только train, меряется только на eval.
//
// Три режима первого уровня закрывают три варианта, разрешённые заданием:
//
//	ngram — символьные n-граммы и ближайшие соседи, без единого вызова
//	embed — эмбеддинги через Ollama, те же соседи
//	tiny  — маленькая языковая модель
//
// Отдельно считается режим llm-only: всё сразу на большой модели, без
// первого уровня. Он нужен как точка отсчёта — без него неясно, потерял ли
// двухуровневый инференс в качестве.
//
// Запуск из корня репозитория:
//
//	go run ./week-2/task-10/cmd/tiered -kind ngram
//	go run ./week-2/task-10/cmd/tiered -kind embed -embed-model nomic-embed-text
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-2/task-10/internal/llm"
	"github.com/swanden/ai-challenge-advanced/week-2/task-10/internal/micro"
	"github.com/swanden/ai-challenge-advanced/week-2/task-10/internal/spec"
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
	ID        string       `json:"id"`
	Group     string       `json:"group"`
	Known     string       `json:"known_class,omitempty"`
	Micro     micro.Answer `json:"micro"`
	Fallback  bool         `json:"fallback"`
	LLMClass  string       `json:"llm_class,omitempty"`
	LLMMS     int64        `json:"llm_latency_ms,omitempty"`
	Final     string       `json:"final"`
	DecidedBy string       `json:"decided_by"`
	Correct   bool         `json:"correct"`
	MicroWas  string       `json:"micro_was,omitempty"` // что сказал первый уровень, если ушли наверх
}

type runReport struct {
	Kind      string         `json:"kind"`
	Params    micro.Params   `json:"params"`
	BigModel  string         `json:"big_model"`
	StartedAt time.Time      `json:"started_at"`
	Records   []record       `json:"records"`
	Totals    map[string]any `json:"totals"`
}

type report struct {
	BaseURL   string      `json:"base_url"`
	TrainPath string      `json:"train_path"`
	TrainSize int         `json:"train_size"`
	StartedAt time.Time   `json:"started_at"`
	Note      string      `json:"note,omitempty"`
	Runs      []runReport `json:"runs"`
}

func main() {
	trainPath := flag.String("train", "week-2/task-6/dataset/train.jsonl", "обучающая выборка Дня 6")
	evalPath := flag.String("eval", "week-2/task-6/dataset/eval.jsonl", "eval Дня 6")
	probesPath := flag.String("probes", "week-2/task-7/dataset/probes.jsonl", "пробы Дня 7")
	baseURL := flag.String("base-url", "http://localhost:11434/v1", "OpenAI-совместимый эндпоинт")
	keyEnv := flag.String("key-env", "", "переменная окружения с ключом")
	kinds := flag.String("kind", "ngram", "первый уровень: ngram, embed, tiny; через запятую — несколько")
	bigModel := flag.String("big", "qwen2.5:7b", "модель второго уровня")
	tinyModel := flag.String("tiny", "qwen2.5:3b", "модель первого уровня для режима tiny")
	embedModel := flag.String("embed-model", "nomic-embed-text", "модель эмбеддингов для режима embed")
	withBaseline := flag.Bool("baseline", true, "прогнать также вариант без первого уровня")
	k := flag.Int("k", micro.Default().K, "сколько соседей учитывать")
	minSim := flag.Float64("min-similarity", micro.Default().MinSimilarity, "порог похожести на ближайший пример")
	minVotes := flag.Float64("min-votes", micro.Default().MinVotes, "порог доли голосов")
	outDir := flag.String("out", "week-2/task-10/evidence", "куда положить отчёт")
	note := flag.String("note", "", "пометка в отчёт")
	flag.Parse()

	sp, evalExamples, err := spec.Load(*evalPath)
	if err != nil {
		fail("%v", err)
	}
	trainExamples, err := spec.LoadExamples(*trainPath)
	if err != nil {
		fail("обучающая выборка: %v", err)
	}
	cases, err := load(sp, evalExamples, *probesPath)
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
	big := llm.New(*baseURL, key, *bigModel)

	fmt.Print("прогрев большой модели… ")
	if _, err := big.Ask(llm.Request{Messages: []llm.Message{{Role: "user", Content: "ok"}}, MaxTokens: 1}); err != nil {
		fail("прогрев: %v", err)
	}
	fmt.Println("готово")

	fmt.Printf("обучающая выборка %s: %d примеров\n", *trainPath, len(trainExamples))
	fmt.Printf("проверка на %d входах, большая модель %s\n\n", len(cases), *bigModel)

	rep := report{
		BaseURL: *baseURL, TrainPath: *trainPath, TrainSize: len(trainExamples),
		StartedAt: time.Now(), Note: *note,
	}

	params := micro.Default()
	params.K = *k
	params.MinSimilarity = *minSim
	params.MinVotes = *minVotes

	if *withBaseline {
		rr, err := runLLMOnly(big, sp, cases)
		rep.Runs = append(rep.Runs, rr)
		if err != nil {
			save(rep, *outDir)
			fail("llm-only: %v", err)
		}
	}

	for _, kind := range strings.Split(*kinds, ",") {
		kind = strings.TrimSpace(kind)
		if kind == "" {
			continue
		}
		p := params
		p.Kind = kind

		var cls *micro.Classifier
		switch kind {
		case micro.KindNGram:
			cls, err = micro.NewNGram(sp, trainExamples, p)
		case micro.KindEmbed:
			cls, err = micro.NewEmbed(sp, trainExamples, p, llm.New(*baseURL, key, *embedModel))
		case micro.KindTiny:
			cls = micro.NewTiny(sp, p, llm.New(*baseURL, key, *tinyModel))
		default:
			fail("неизвестный вид первого уровня %q", kind)
		}
		if err != nil {
			fail("обучение %s: %v", kind, err)
		}

		rr, err := run(cls, big, sp, p, *bigModel, cases)
		rep.Runs = append(rep.Runs, rr)
		if err != nil {
			save(rep, *outDir)
			fail("режим %s: %v", kind, err)
		}
	}

	compare(rep)
	if path := save(rep, *outDir); path != "" {
		fmt.Printf("\nотчёт: %s\n", path)
	}
}

// runLLMOnly — вариант без первого уровня, точка отсчёта.
func runLLMOnly(big *llm.Client, sp *spec.Spec, cases []testCase) (runReport, error) {
	rr := runReport{Kind: "llm-only", BigModel: big.Model, StartedAt: time.Now()}
	fmt.Printf("══ без первого уровня ══\n")
	for _, tc := range cases {
		class, ms, err := askBig(big, sp, tc.User)
		rec := record{ID: tc.ID, Group: tc.Group, Known: tc.Class, Fallback: true,
			LLMClass: class, LLMMS: ms, Final: class, DecidedBy: "llm"}
		if tc.Class != "" {
			rec.Correct = class == tc.Class
		}
		rr.Records = append(rr.Records, rec)
		if err != nil {
			return rr, err
		}
	}
	summarize(&rr)
	printTotals(rr)
	fmt.Println()
	return rr, nil
}

func run(cls *micro.Classifier, big *llm.Client, sp *spec.Spec, p micro.Params, bigModel string, cases []testCase) (runReport, error) {
	rr := runReport{Kind: p.Kind, Params: p, BigModel: bigModel, StartedAt: time.Now()}
	fmt.Printf("══ первый уровень: %s ══\n", p.Kind)

	group := ""
	for _, tc := range cases {
		if tc.Group != group {
			group = tc.Group
			fmt.Printf("── %s ──\n", group)
		}

		a, err := cls.Classify(tc.User)
		rec := record{ID: tc.ID, Group: tc.Group, Known: tc.Class, Micro: a}
		if err != nil {
			rr.Records = append(rr.Records, rec)
			return rr, err
		}

		if a.Status == micro.StatusOK {
			rec.Final = a.Class
			rec.DecidedBy = "micro"
		} else {
			rec.Fallback = true
			rec.MicroWas = a.Class
			class, ms, err := askBig(big, sp, tc.User)
			rec.LLMClass = class
			rec.LLMMS = ms
			rec.Final = class
			rec.DecidedBy = "llm"
			if err != nil {
				rr.Records = append(rr.Records, rec)
				return rr, err
			}
		}
		if tc.Class != "" {
			rec.Correct = rec.Final == tc.Class
		}
		rr.Records = append(rr.Records, rec)
		printRecord(rec)
	}

	summarize(&rr)
	printTotals(rr)
	fmt.Println()
	return rr, nil
}

func askBig(c *llm.Client, sp *spec.Spec, input string) (string, int64, error) {
	ans, err := c.Ask(llm.Request{
		Messages: []llm.Message{
			{Role: "system", Content: sp.SystemPrompt},
			{Role: "user", Content: input},
		},
		Temperature: 0, MaxTokens: 16, Seed: 1,
	})
	if err != nil {
		return "", ans.LatencyMS, err
	}
	class, _ := sp.ParseClass(ans.Raw)
	return class, ans.LatencyMS, nil
}

func printRecord(r record) {
	mark := " "
	if r.Known != "" {
		mark = "×"
		if r.Correct {
			mark = "✓"
		}
	}
	where := "micro"
	extra := fmt.Sprintf("%.2f", r.Micro.Confidence)
	if r.Fallback {
		where = "→ llm"
		extra = r.Micro.Reason
		if r.MicroWas != "" {
			extra = fmt.Sprintf("micro хотел %s, %s", r.MicroWas, r.Micro.Reason)
		}
	}
	fmt.Printf("%s %-12s %-7s %-16s %s\n", mark, r.ID, where, orDash(r.Final), extra)
}

func summarize(rr *runReport) {
	t := map[string]any{}
	handled, fallback := 0, 0
	var microUS, llmMS int64
	known, correct := 0, 0
	microKnown, microCorrect := 0, 0
	llmKnown, llmCorrect := 0, 0
	microWouldBeRight := 0

	for _, r := range rr.Records {
		microUS += r.Micro.LatencyUS
		llmMS += r.LLMMS
		if r.Fallback {
			fallback++
		} else {
			handled++
		}
		if r.Known == "" {
			continue
		}
		known++
		if r.Correct {
			correct++
		}
		if r.Fallback {
			llmKnown++
			if r.Correct {
				llmCorrect++
			}
			// Была ли эскалация оправданной: угадал бы первый
			// уровень, если бы ему доверились.
			if r.MicroWas == r.Known {
				microWouldBeRight++
			}
		} else {
			microKnown++
			if r.Correct {
				microCorrect++
			}
		}
	}

	t["входов"] = len(rr.Records)
	t["обработал micro"] = handled
	t["ушло в fallback"] = fallback
	t["вызовов большой модели"] = fallback
	// Обе величины сводятся к миллисекундам явно: microUS хранится в
	// микросекундах, llmMS в миллисекундах, и складывать их напрямую
	// нельзя — в первой версии здесь была именно такая ошибка.
	microMS := float64(microUS) / 1000
	t["задержка micro суммарно мс"] = microMS
	t["задержка llm суммарно мс"] = llmMS
	if n := len(rr.Records); n > 0 {
		t["средняя задержка micro мс"] = microMS / float64(n)
		t["средняя задержка llm мс"] = float64(llmMS) / float64(n)
		t["средняя задержка на вход мс"] = (microMS + float64(llmMS)) / float64(n)
	}
	if known > 0 {
		t["входов с известным ответом"] = known
		t["точность общая"] = float64(correct) / float64(known)
	}
	if microKnown > 0 {
		t["решено micro (с известным ответом)"] = microKnown
		t["точность micro на своих"] = float64(microCorrect) / float64(microKnown)
	}
	if llmKnown > 0 {
		t["ушло наверх (с известным ответом)"] = llmKnown
		t["точность llm на переданных"] = float64(llmCorrect) / float64(llmKnown)
		t["эскалаций, где micro был бы прав"] = microWouldBeRight
	}
	rr.Totals = t
}

func printTotals(rr runReport) {
	t := rr.Totals
	fmt.Printf("\nвходов %v: обработал micro %v, ушло наверх %v\n",
		t["входов"], t["обработал micro"], t["ушло в fallback"])
	fmt.Printf("вызовов большой модели %v\n", t["вызовов большой модели"])
	if v, ok := t["средняя задержка micro мс"]; ok {
		fmt.Printf("задержка на вход: micro %.3f мс, llm %.1f мс, всего %.1f мс\n",
			v.(float64), t["средняя задержка llm мс"].(float64), t["средняя задержка на вход мс"].(float64))
	}
	if v, ok := t["точность общая"]; ok {
		fmt.Printf("точность общая %.0f%%\n", v.(float64)*100)
	}
	if v, ok := t["точность micro на своих"]; ok {
		fmt.Printf("  micro решил %v, из них верно %.0f%%\n", t["решено micro (с известным ответом)"], v.(float64)*100)
	}
	if v, ok := t["точность llm на переданных"]; ok {
		fmt.Printf("  llm получил %v, из них верно %.0f%%", t["ушло наверх (с известным ответом)"], v.(float64)*100)
		fmt.Printf(", при этом micro был бы прав в %v случаях\n", t["эскалаций, где micro был бы прав"])
	}
}

func compare(rep report) {
	if len(rep.Runs) < 2 {
		return
	}
	fmt.Printf("\n══ сравнение ══\n")
	fmt.Printf("%-10s %10s %14s %16s\n", "вариант", "точность", "вызовов LLM", "задержка/вход мс")
	for _, r := range rep.Runs {
		acc := "—"
		if v, ok := r.Totals["точность общая"]; ok {
			acc = fmt.Sprintf("%.0f%%", v.(float64)*100)
		}
		lat := "—"
		if v, ok := r.Totals["средняя задержка на вход мс"]; ok {
			lat = fmt.Sprintf("%.1f", v.(float64))
		}
		fmt.Printf("%-10s %10s %14v %16s\n", r.Kind, acc, r.Totals["вызовов большой модели"], lat)
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
		tc := testCase{ID: p.ID, Group: p.Kind, User: p.User, Class: p.Class}
		switch p.Kind {
		case "borderline":
			borderline = append(borderline, tc)
		case "noisy":
			noisy = append(noisy, tc)
		}
	}
	return append(append(cases, borderline...), noisy...), nil
}

func save(rep report, outDir string) string {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "tiered: отчёт не сохранён: %v\n", err)
		return ""
	}
	path := filepath.Join(outDir, fmt.Sprintf("tiered-%s.json", rep.StartedAt.Format("20060102-150405")))
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tiered: отчёт не сохранён: %v\n", err)
		return ""
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "tiered: отчёт не сохранён: %v\n", err)
		return ""
	}
	return path
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "tiered: "+format+"\n", args...)
	os.Exit(1)
}
