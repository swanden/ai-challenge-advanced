// Команда confidence прогоняет классификатор Дня 6 через контроль принятия
// результата и замеряет, чего этот контроль стоит и что он ловит.
//
// Входы делятся на три группы, как требует задание:
//
//	correct    — шестнадцать примеров из eval Дня 6, у каждого известен верный класс
//	borderline — формулировки, честно допускающие два класса
//	noisy      — мусор, обрезки, чужой язык, попытка инъекции
//
// Для correct известна истина, поэтому по ним считается главное: ловит ли
// контроль настоящие ошибки модели и не отвергает ли верные ответы. Для
// остальных известен ожидаемый вердикт, а не класс.
//
// Запуск из корня репозитория:
//
//	go run ./week-2/task-7/cmd/confidence \
//	    -base-url http://localhost:11434/v1 -key-env "" -model qwen2.5:7b
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

	"github.com/swanden/ai-challenge-advanced/week-2/task-7/internal/gate"
	"github.com/swanden/ai-challenge-advanced/week-2/task-7/internal/llm"
	"github.com/swanden/ai-challenge-advanced/week-2/task-7/internal/spec"
)

// probe — строка probes.jsonl.
type probe struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Expect string `json:"expect"` // accept или reject
	Class  string `json:"class,omitempty"`
	User   string `json:"user"`
	Why    string `json:"why"`
}

// testCase — один вход независимо от источника.
type testCase struct {
	ID     string
	Group  string // correct, borderline, noisy
	User   string
	Expect string // accept или reject
	Class  string // известный верный класс, если он есть
	Why    string
}

// record — результат по одному входу вместе с разбором.
type record struct {
	ID       string      `json:"id"`
	Group    string      `json:"group"`
	Expect   string      `json:"expect"`
	Class    string      `json:"known_class,omitempty"`
	Why      string      `json:"why,omitempty"`
	Result   gate.Result `json:"result"`
	Outcome  string      `json:"outcome"`
	AsMeant  bool        `json:"as_meant"`
	MonoOK   bool        `json:"monolithic_correct"`
	GateKept bool        `json:"gate_kept"`
}

// truthStats — метрики по группе, где известен верный ответ.
//
// Ключевая величина здесь — точность среди принятых: именно она отвечает,
// окупился ли контроль. Точность вообще показывает, каков был бы результат
// без всякого контроля, разница между ними и есть его вклад.
type truthStats struct {
	Total       int     `json:"входов с известным ответом"`
	MonoCorrect int     `json:"монолит верен"`
	Kept        int     `json:"принято и верно"`
	Missed      int     `json:"принято, ошибка пропущена"`
	Caught      int     `json:"отклонено, ошибка поймана"`
	FalseAlarm  int     `json:"отклонено, ложная тревога"`
	AccAll      float64 `json:"точность вообще"`
	AccAccepted float64 `json:"точность среди принятых"`
	Lift        float64 `json:"вклад контроля"`
}

type report struct {
	Model      string          `json:"model"`
	BaseURL    string          `json:"base_url"`
	StartedAt  time.Time       `json:"started_at"`
	System     string          `json:"system_prompt"`
	Classes    []string        `json:"classes"`
	Thresholds gate.Thresholds `json:"thresholds"`
	Records    []record       `json:"records"`
	Totals     map[string]any `json:"totals"`
	Note       string         `json:"note,omitempty"`
}

func main() {
	evalPath := flag.String("eval", "week-2/task-6/dataset/eval.jsonl", "eval Дня 6")
	probesPath := flag.String("probes", "week-2/task-7/dataset/probes.jsonl", "пограничные и шумные входы")
	model := flag.String("model", "qwen2.5:7b", "модель")
	baseURL := flag.String("base-url", "http://localhost:11434/v1", "OpenAI-совместимый эндпоинт")
	keyEnv := flag.String("key-env", "", "переменная окружения с ключом; пустая — без авторизации")
	outDir := flag.String("out", "week-2/task-7/evidence", "куда положить отчёт")
	note := flag.String("note", "", "пометка в отчёт")
	minSeqProb := flag.Float64("min-seq-prob", gate.Default().MinSeqProb, "порог вероятности ответа")
	minMargin := flag.Float64("min-margin", gate.Default().MinMargin, "порог отрыва от альтернативы")
	samples := flag.Int("samples", gate.Default().Samples, "сколько выборок берёт слой избыточности")
	sampleTemp := flag.Float64("sample-temp", gate.Default().SampleTemp, "температура выборок")
	minAgreement := flag.Int("min-agreement", gate.Default().MinAgreement, "сколько выборок должны совпасть")
	sampleMode := flag.String("sample-mode", gate.Default().SampleMode,
		"как берутся выборки: shuffle — проворот списка классов, temperature — температура")
	flag.Parse()

	th := gate.Thresholds{
		MinSeqProb:   *minSeqProb,
		MinMargin:    *minMargin,
		Samples:      *samples,
		SampleTemp:   *sampleTemp,
		MinAgreement: *minAgreement,
		MinInputLen:  gate.Default().MinInputLen,
		SampleMode:   *sampleMode,
	}
	if th.SampleMode != gate.SampleShuffle && th.SampleMode != gate.SampleTemperature {
		fail("неизвестный -sample-mode %q, ожидалось %s или %s", th.SampleMode, gate.SampleShuffle, gate.SampleTemperature)
	}

	sp, examples, err := spec.Load(*evalPath)
	if err != nil {
		fail("%v", err)
	}
	cases, err := load(sp, examples, *probesPath)
	if err != nil {
		fail("%v", err)
	}
	fmt.Printf("контракт восстановлен из %s: %d классов — %s\n", sp.Source, len(sp.Classes), sp.List())

	var key string
	if *keyEnv != "" {
		key = os.Getenv(*keyEnv)
		if key == "" {
			fail("переменная %s пуста", *keyEnv)
		}
	}
	client := llm.New(*baseURL, key, *model)

	rep := report{
		Model:      *model,
		BaseURL:    *baseURL,
		StartedAt:  time.Now(),
		System:     sp.SystemPrompt,
		Classes:    sp.Classes,
		Thresholds: th,
		Note:       *note,
	}

	switch th.SampleMode {
	case gate.SampleShuffle:
		fmt.Printf("модель %s, порог вероятности %.2f, порог отрыва %.2f, выборок %d с проворотом списка классов\n\n",
			*model, th.MinSeqProb, th.MinMargin, th.Samples)
	default:
		fmt.Printf("модель %s, порог вероятности %.2f, порог отрыва %.2f, выборок %d при температуре %.1f\n\n",
			*model, th.MinSeqProb, th.MinMargin, th.Samples, th.SampleTemp)
	}

	group := ""
	for _, tc := range cases {
		if tc.Group != group {
			group = tc.Group
			fmt.Printf("── %s ──\n", group)
		}
		res, err := gate.Evaluate(client, sp, th, tc.User)
		if err != nil {
			// Прогон не бросаем: пишем то, что успели, и выходим по-человечески.
			rep.Records = append(rep.Records, record{ID: tc.ID, Group: tc.Group, Result: res, Outcome: "ошибка вызова: " + err.Error()})
			summarize(&rep)
			save(rep, *outDir)
			fail("%s: %v", tc.ID, err)
		}

		rec := record{
			ID: tc.ID, Group: tc.Group, Expect: tc.Expect,
			Class: tc.Class, Why: tc.Why, Result: res,
		}
		accepted := res.Status == gate.StatusOK
		if tc.Class != "" {
			// Там, где истина известна, «как ожидалось» означает разумное
			// поведение контроля, а не безусловное принятие: поймать
			// настоящую ошибку — лучший исход, а не промах.
			rec.MonoOK = res.Class == tc.Class
			rec.GateKept = accepted && rec.MonoOK
			rec.AsMeant = accepted == rec.MonoOK
			switch {
			case accepted && rec.MonoOK:
				rec.Outcome = "принято, верно"
			case accepted && !rec.MonoOK:
				rec.Outcome = "принято, ошибка пропущена"
			case !accepted && !rec.MonoOK:
				rec.Outcome = "отклонено, ошибка поймана"
			default:
				rec.Outcome = "отклонено, ложная тревога"
			}
		} else {
			rec.AsMeant = (accepted && tc.Expect == "accept") || (!accepted && tc.Expect == "reject")
			if accepted {
				rec.Outcome = "принято"
			} else {
				rec.Outcome = "отклонено"
			}
		}
		rep.Records = append(rep.Records, rec)

		mark := "✓"
		if !rec.AsMeant {
			mark = "×"
		}
		extra := ""
		if res.SelfCheckRan {
			extra = ", самопроверка"
		}
		fmt.Printf("%s %-12s %-6s %-16s p=%.2f отрыв=%.2f согласие=%d/%d%s — %s\n",
			mark, tc.ID, res.Status, orDash(res.Class),
			res.SeqProb, res.Margin, res.Agreement, th.Samples, extra, rec.Outcome)
	}

	summarize(&rep)
	printTotals(rep)
	if path := save(rep, *outDir); path != "" {
		fmt.Printf("\nотчёт: %s\n", path)
	}
}

func load(sp *spec.Spec, examples []spec.Example, probesPath string) ([]testCase, error) {
	var cases []testCase

	for i, ex := range examples {
		cases = append(cases, testCase{
			ID:     fmt.Sprintf("eval-%02d", i+1),
			Group:  "correct",
			User:   ex.User(),
			Expect: "accept",
			Class:  ex.Class(),
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
		if p.Expect != "accept" && p.Expect != "reject" {
			return nil, fmt.Errorf("%s: у %s поле expect равно %q, ожидалось accept или reject", probesPath, p.ID, p.Expect)
		}
		if p.Class != "" && !sp.IsClass(p.Class) {
			return nil, fmt.Errorf("%s: у %s класс %q вне контракта", probesPath, p.ID, p.Class)
		}
		tc := testCase{ID: p.ID, Group: p.Kind, User: p.User, Expect: p.Expect, Class: p.Class, Why: p.Why}
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

func summarize(rep *report) {
	t := map[string]any{}

	byGroup := map[string]map[string]int{}
	var calls, prompt, completion int
	var latency int64
	selfChecks := 0
	rejected := 0
	preInference := 0

	byTruth := map[string]*truthStats{}

	for _, r := range rep.Records {
		g := byGroup[r.Group]
		if g == nil {
			g = map[string]int{}
			byGroup[r.Group] = g
		}
		g["всего"]++
		g[string(r.Result.Status)]++
		if r.AsMeant {
			g["как ожидалось"]++
		}

		calls += r.Result.Calls
		prompt += r.Result.PromptTokens
		completion += r.Result.CompletionTokens
		latency += r.Result.LatencyMS
		if r.Result.SelfCheckRan {
			selfChecks++
		}
		if r.Result.Status != gate.StatusOK {
			rejected++
		}
		if !r.Result.InputOK {
			preInference++
		}

		if r.Class != "" {
			tr := byTruth[r.Group]
			if tr == nil {
				tr = &truthStats{}
				byTruth[r.Group] = tr
			}
			tr.Total++
			if r.MonoOK {
				tr.MonoCorrect++
			}
			switch r.Outcome {
			case "принято, верно":
				tr.Kept++
			case "принято, ошибка пропущена":
				tr.Missed++
			case "отклонено, ошибка поймана":
				tr.Caught++
			case "отклонено, ложная тревога":
				tr.FalseAlarm++
			}
		}
	}

	t["по группам"] = byGroup
	t["всего входов"] = len(rep.Records)
	t["отклонено"] = rejected
	t["отклонено до инференса"] = preInference
	t["повторный инференс (самопроверок)"] = selfChecks
	t["вызовов модели"] = calls
	t["токенов промпта"] = prompt
	t["токенов ответа"] = completion
	t["суммарная задержка мс"] = latency
	if len(rep.Records) > 0 {
		t["вызовов на вход"] = float64(calls) / float64(len(rep.Records))
		t["задержка на вход мс"] = latency / int64(len(rep.Records))
	}
	if len(byTruth) > 0 {
		known := map[string]truthStats{}
		for group, tr := range byTruth {
			st := *tr
			if st.Total > 0 {
				st.AccAll = float64(st.MonoCorrect) / float64(st.Total)
			}
			if accepted := st.Kept + st.Missed; accepted > 0 {
				st.AccAccepted = float64(st.Kept) / float64(accepted)
			}
			st.Lift = st.AccAccepted - st.AccAll
			known[group] = st
		}
		t["известная истина по группам"] = known
	}
	rep.Totals = t
}

func printTotals(rep report) {
	t := rep.Totals
	fmt.Printf("\n── итоги ──\n")
	fmt.Printf("способ выборок: %s\n", rep.Thresholds.SampleMode)
	fmt.Printf("входов %v, отклонено %v (из них %v до инференса)\n",
		t["всего входов"], t["отклонено"], t["отклонено до инференса"])
	fmt.Printf("самопроверка понадобилась %v раз\n", t["повторный инференс (самопроверок)"])
	fmt.Printf("вызовов модели %v, в среднем %.2f на вход\n", t["вызовов модели"], t["вызовов на вход"])
	fmt.Printf("токенов: промпт %v, ответ %v\n", t["токенов промпта"], t["токенов ответа"])
	fmt.Printf("задержка: суммарно %v мс, в среднем %v мс на вход\n",
		t["суммарная задержка мс"], t["задержка на вход мс"])

	if raw, ok := t["известная истина по группам"]; ok {
		known, _ := raw.(map[string]truthStats)
		groups := make([]string, 0, len(known))
		for g := range known {
			groups = append(groups, g)
		}
		sort.Strings(groups)
		for _, g := range groups {
			k := known[g]
			fmt.Printf("\nгруппа %s, входов с известным ответом %d:\n", g, k.Total)
			fmt.Printf("  монолитный ответ верен:    %d\n", k.MonoCorrect)
			fmt.Printf("  принято и верно:           %d\n", k.Kept)
			fmt.Printf("  принято, ошибка пропущена: %d\n", k.Missed)
			fmt.Printf("  отклонено, ошибка поймана: %d\n", k.Caught)
			fmt.Printf("  отклонено, ложная тревога: %d\n", k.FalseAlarm)
			fmt.Printf("  точность вообще %.0f%%, среди принятых %.0f%%, вклад контроля %+.0f пунктов\n",
				k.AccAll*100, k.AccAccepted*100, k.Lift*100)
		}
	}
}

func save(rep report, outDir string) string {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "confidence: отчёт не сохранён: %v\n", err)
		return ""
	}
	name := fmt.Sprintf("confidence-%s-%s.json",
		strings.NewReplacer("/", "_", ":", "_").Replace(rep.Model),
		rep.StartedAt.Format("20060102-150405"))
	path := filepath.Join(outDir, name)
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "confidence: отчёт не сохранён: %v\n", err)
		return ""
	}
	if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "confidence: отчёт не сохранён: %v\n", err)
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
	fmt.Fprintf(os.Stderr, "confidence: "+format+"\n", args...)
	os.Exit(1)
}
