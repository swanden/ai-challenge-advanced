// Команда probe прогоняет наборы тест-кейсов через политику гейтвея и
// проверяет журнал на утечки.
//
// Наборов два, и считаются они раздельно. Причина — находка Artofpaganini,
// который мерил свой гейтвей четырежды: тесты, написанные автором кода, дали
// 100%, слепой набор от постороннего — 45%, после починки 84% (но набор был уже
// «подсказан»), а свежий слепой — 69%. Его формулировка: дважды поймали себя на
// том, что меряют не защиту, а собственную память о том, где она дырявая.
//
// Здесь та же ловушка стоит по умолчанию: детекторы и набор cases.jsonl писал
// один автор. Поэтому цифра по нему объявляется заявленной, а не измеренной, и
// рядом всегда идёт слепой набор blind.jsonl, наполняемый со стороны.
//
//	go run ./week-3/task-13/cmd/probe
//	go run ./week-3/task-13/cmd/probe -cases week-3/task-13/dataset/blind.jsonl
//	go run ./week-3/task-13/cmd/probe -check-logs
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-3/task-13/internal/detect"
	"github.com/swanden/ai-challenge-advanced/week-3/task-13/internal/policy"
)

// Case — один тест-кейс.
type Case struct {
	ID     string `json:"id"`
	Side   string `json:"side"` // input или output
	Text   string `json:"text"`
	System string `json:"system,omitempty"`
	Expect string `json:"expect"` // pass, masked, blocked
	Why    string `json:"why,omitempty"`
	// Known — заведомо непойманное, зафиксированное честно. Такие кейсы
	// считаются отдельно и в общую долю не идут скрытно.
	Known bool `json:"known_miss,omitempty"`
}

func main() {
	var (
		casesPath = flag.String("cases", "week-3/task-13/dataset/cases.jsonl", "набор кейсов")
		mode      = flag.String("mode", "mask", "режим входного слоя")
		checkLogs = flag.Bool("check-logs", false, "искать в журнале известные тестовые секреты")
		logDir    = flag.String("logs", "week-3/task-13/logs", "каталог журнала")
		outDir    = flag.String("out", "week-3/task-13/evidence", "куда сохранить отчёт прогона")
		note      = flag.String("note", "", "пометка в отчёте")
	)
	flag.Parse()

	if *checkLogs {
		if err := checkLeaks(*logDir, *casesPath); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}

	cases, err := load(*casesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	blind := strings.Contains(*casesPath, "blind")
	rep := Report{
		At:        time.Now().Format(time.RFC3339),
		CasesFile: *casesPath,
		Mode:      *mode,
		Blind:     blind,
		Note:      *note,
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "кейс\tсторона\tждали\tполучили\tитог\tчто нашлось\n")

	var ok, bad, known int
	for _, c := range cases {
		var v policy.Verdict
		if c.Side == "output" {
			v = policy.ScanOutput(c.Text, c.System, policy.Mode(*mode))
		} else {
			v = policy.ScanInput(c.Text, policy.Mode(*mode))
		}
		mark := "ок"
		switch {
		case v.Action == c.Expect:
			ok++
		case c.Known:
			mark = "известный пропуск"
			known++
		default:
			mark = "РАСХОЖДЕНИЕ"
			bad++
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			c.ID, c.Side, c.Expect, v.Action, mark, short(detect.Describe(v.Findings)))

		// В отчёт идут находки, а не тексты кейсов: кейсы содержат секреты,
		// пусть и выдуманные, и складывать их в evidence незачем.
		rep.Results = append(rep.Results, Result{
			ID: c.ID, Side: c.Side, Expect: c.Expect, Got: v.Action,
			Verdict: mark, Known: c.Known, Findings: v.Findings, Why: c.Why,
		})
	}
	tw.Flush()

	total := len(cases)
	fmt.Printf("\nсовпало %d из %d", ok, total)
	if known > 0 {
		fmt.Printf(", заведомо непойманных %d", known)
	}
	if bad > 0 {
		fmt.Printf(", РАСХОЖДЕНИЙ %d", bad)
	}
	fmt.Printf("\nдоля: %.0f%%\n", float64(ok)/float64(total)*100)

	rep.Total, rep.Matched, rep.KnownMiss, rep.Mismatch = total, ok, known, bad
	rep.Share = float64(ok) / float64(total) * 100

	if blind {
		rep.Kind = "слепой"
		fmt.Println("\nЭто слепой набор: составлялся без доступа к коду детекторов.")
		fmt.Println("Его доля и есть измеренная величина.")
	} else {
		rep.Kind = "собственный"
		fmt.Println("\nЭто собственный набор: детекторы и кейсы писал один автор.")
		fmt.Println("Его доля — заявленная, а не измеренная. Настоящая цифра — по слепому набору.")
	}

	path, err := rep.Save(*outDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "отчёт не сохранён: %v\n", err)
		return
	}
	fmt.Printf("\nОтчёт: %s\n", path)
}

// Result — исход одного кейса.
type Result struct {
	ID       string           `json:"id"`
	Side     string           `json:"side"`
	Expect   string           `json:"expect"`
	Got      string           `json:"got"`
	Verdict  string           `json:"verdict"`
	Known    bool             `json:"known_miss,omitempty"`
	Findings []detect.Finding `json:"findings,omitempty"`
	Why      string           `json:"why,omitempty"`
}

// Report — сохраняемый результат прогона.
//
// Нужен по той же причине, что evidence в Днях 11 и 12: цифра в отчёте должна
// опираться на файл, а не на строку в терминале. Особенно это важно для слепого
// набора — он прогоняется один раз и чужими руками, переспросить будет негде.
type Report struct {
	At        string   `json:"at"`
	Kind      string   `json:"kind"`
	Blind     bool     `json:"blind"`
	CasesFile string   `json:"cases_file"`
	Mode      string   `json:"mode"`
	Note      string   `json:"note,omitempty"`
	Total     int      `json:"total"`
	Matched   int      `json:"matched"`
	KnownMiss int      `json:"known_miss"`
	Mismatch  int      `json:"mismatch"`
	Share     float64  `json:"share_percent"`
	Results   []Result `json:"results"`
}

// Save пишет отчёт в каталог.
func (r Report) Save(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	kind := "cases"
	if r.Blind {
		kind = "blind"
	}
	path := filepath.Join(dir,
		fmt.Sprintf("%s-%s.json", time.Now().Format("20060102-150405"), kind))
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, b, 0o644)
}

// checkLeaks ищет в журнале секреты из тест-кейсов.
//
// Проверка существует потому, что у dpmn гейтвей писал заблокированный ключ в
// аудит-лог открытым текстом. Инструмент, поставленный ради предотвращения
// утечки, устраивал её сам, и обнаружилось это только grep-ом.
func checkLeaks(logDir, casesPath string) error {
	cases, err := load(casesPath)
	if err != nil {
		return err
	}
	var secrets []string
	for _, c := range cases {
		findings, _ := detect.Scan(c.Text)
		for range findings {
			for _, r := range detect.Rules {
				_ = r
			}
		}
		// Берём сами исходные строки: в журнале не должно быть ни одной.
		for _, tok := range strings.Fields(c.Text) {
			tok = strings.Trim(tok, `"',.;:()`)
			if len(tok) < 16 {
				continue
			}
			if f, _ := detect.Scan(tok); len(f) > 0 {
				secrets = append(secrets, tok)
			}
		}
	}
	sort.Strings(secrets)

	files, err := filepath.Glob(filepath.Join(logDir, "*.jsonl"))
	if err != nil {
		return err
	}
	if len(files) == 0 {
		fmt.Println("журналов нет — проверять нечего")
		return nil
	}
	leaks := 0
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return err
		}
		body := string(b)
		for _, s := range secrets {
			if strings.Contains(body, s) {
				fmt.Printf("УТЕЧКА В ЖУРНАЛ: %s содержит %s\n", f, s)
				leaks++
			}
		}
	}
	fmt.Printf("проверено файлов: %d, известных секретов: %d, утечек: %d\n",
		len(files), len(secrets), leaks)
	if leaks > 0 {
		return fmt.Errorf("журнал содержит секреты")
	}
	return nil
}

func load(path string) ([]Case, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Case
	for i, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var c Case
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("%s строка %d: %w", path, i+1, err)
		}
		if c.Side == "" {
			c.Side = "input"
		}
		out = append(out, c)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: ни одного кейса", path)
	}
	return out, nil
}

func short(s string) string {
	r := []rune(s)
	if len(r) <= 60 {
		return s
	}
	return string(r[:60]) + "…"
}
