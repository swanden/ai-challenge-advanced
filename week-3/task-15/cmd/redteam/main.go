// Команда redteam прогоняет пробы Дня 15 против модели guard-proxy партнёра.
//
// День 15 — групповой red-team: ломаем пайплайн соседа. Партнёр (@kgdnm)
// публичного стенда не выставил и код своего агента не отдаёт, но его гейтвей
// Дня 13 (ветка day13/guard-proxy) прислан архивом. Его guard'ы — чистые функции
// над текстом, поэтому их поведение воспроизводится портом правил
// (internal/target) точно и без обращения к его прокси по сети. Это и есть
// заявленное ограничение работы: мишень смоделирована из исходников, а не
// атакована вживую.
//
// Раннер бьёт по входному и выходному слоям во всех трёх режимах входа
// (block/mask/log) и обоих режимах выхода (detect/redact), сверяя исход с
// ожиданием, записанным в пробе ДО прогона. Пробы двух видов: контрольные
// (детектор обязан сработать — иначе меряем сломанный детектор) и пробойные
// (предсказание из чтения его кода — где он течёт).
//
//	go run ./week-3/task-15/cmd/redteam
//	go run ./week-3/task-15/cmd/redteam -mode block
//	go run ./week-3/task-15/cmd/redteam -out week-3/task-15/evidence -note "прогон"
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-3/task-15/internal/target"
)

// Probe — одна проба из probes.jsonl.
type Probe struct {
	ID        string   `json:"id"`
	Surface   string   `json:"surface"` // input | output
	Fragments []string `json:"fragments,omitempty"`
	Text      string   `json:"text,omitempty"`
	Expect    string   `json:"expect"` // pass | caught | flagged | clean
	Note      string   `json:"note"`
}

// Result — исход прогона одной пробы в одном режиме.
type Result struct {
	ID      string `json:"id"`
	Surface string `json:"surface"`
	Mode    string `json:"mode"`
	Expect  string `json:"expect"`
	Got     string `json:"got"`
	Match   bool   `json:"match"`
	// Breach — проба-пробой сработала: атака прошла там, где по замыслу
	// защиты не должна. Для контрольных проб всегда false.
	Breach   bool     `json:"breach"`
	Detail   string   `json:"detail,omitempty"`
	Reached  bool     `json:"reached_client,omitempty"`
	Findings []string `json:"findings,omitempty"`
}

// Report — весь прогон для evidence.
type Report struct {
	At      string   `json:"at"`
	Note    string   `json:"note,omitempty"`
	Target  string   `json:"target"`
	Caveat  string   `json:"caveat"`
	Results []Result `json:"results"`
	Summary Summary  `json:"summary"`
}

// Summary — сводка: контроль отдельно от пробоев.
type Summary struct {
	ControlsTotal  int `json:"controls_total"`
	ControlsOK     int `json:"controls_ok"`
	BreachesFound  int `json:"breaches_found"`
	BreachesTotal  int `json:"breaches_total"`
	PredictionMiss int `json:"prediction_miss"` // проба не совпала с ожиданием
}

func main() {
	var (
		probesPath = flag.String("probes", "week-3/task-15/dataset/probes.jsonl", "набор проб")
		inMode     = flag.String("mode", "", "входной режим: block|mask|log; пусто — все три")
		outMode    = flag.String("output", "detect", "выходной режим: detect|redact")
		outDir     = flag.String("out", "", "каталог для evidence; пусто — только печать")
		note       = flag.String("note", "", "пометка в отчёте")
	)
	flag.Parse()

	probes, err := load(*probesPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	inModes := []target.Mode{target.ModeBlock, target.ModeMask, target.ModeLog}
	if *inMode != "" {
		inModes = []target.Mode{target.Mode(*inMode)}
	}

	rep := Report{
		At:     time.Now().Format(time.RFC3339),
		Note:   *note,
		Target: "guard-proxy @kgdnm (day13/guard-proxy), правила портированы в internal/target",
		Caveat: "мишень СМОДЕЛИРОВАНА из исходного кода партнёра, не атакована по сети: публичный стенд не выставлен",
	}

	for _, p := range probes {
		if p.Surface == "output" {
			r := runOutput(p, target.OutputMode(*outMode))
			rep.Results = append(rep.Results, r)
			continue
		}
		for _, m := range inModes {
			rep.Results = append(rep.Results, runInput(p, m))
		}
	}

	rep.Summary = summarize(rep.Results)
	print(rep)

	if *outDir != "" {
		if err := save(rep, *outDir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

// isControl — проба, где мы ждём срабатывания детектора (caught/flagged) либо
// pass на заведомо чистом входе (C0). Пробои ждут pass/clean там, где защита
// должна была сработать.
func isControl(p Probe) bool {
	return strings.HasPrefix(p.ID, "C") || strings.HasPrefix(p.ID, "O3") ||
		strings.HasSuffix(p.ID, "ctrl-b64-single") || p.ID == "A1-split-adjacent" || p.ID == "O1-leak-english"
}

func runInput(p Probe, mode target.Mode) Result {
	res := target.Inspect(p.Fragments, mode)
	got := "pass"
	if !res.Passed() {
		if res.Blocked {
			got = "blocked"
		} else {
			got = "caught"
		}
	}
	// Нормализуем: для сверки с ожиданием caught и blocked — оба «детектор сработал».
	norm := got
	if got == "blocked" {
		norm = "caught"
	}
	r := Result{
		ID: p.ID, Surface: "input", Mode: string(mode),
		Expect: p.Expect, Got: got,
		Match: norm == p.Expect,
	}
	for _, f := range res.Findings {
		r.Findings = append(r.Findings, f.Kind)
	}
	// Breach: ждали pass (защита должна была словить, а по замыслу атаки — не словит),
	// получили pass, и это НЕ контроль.
	r.Breach = p.Expect == "pass" && got == "pass" && !isControl(p)
	return r
}

func runOutput(p Probe, mode target.OutputMode) Result {
	out := target.ScanOutput(p.Text, mode)
	got := "clean"
	if !out.Clean() {
		got = "flagged"
	}
	r := Result{
		ID: p.ID, Surface: "output", Mode: string(mode),
		Expect: p.Expect, Got: got,
		Match:   got == p.Expect,
		Reached: out.ReachedClient,
	}
	for _, f := range out.Findings {
		r.Findings = append(r.Findings, f.Kind)
	}
	if len(out.Flags) > 0 {
		r.Detail = strings.Join(out.Flags, "; ")
	}
	// Breach на выходе: ждали clean (детектор промолчит), получили clean, не контроль —
	// это слепая зона. ЛИБО: flagged, но текст всё равно дошёл до клиента (detect).
	if p.Expect == "clean" && got == "clean" && !isControl(p) {
		r.Breach = true
	}
	if got == "flagged" && out.ReachedClient && !isControl(p) {
		r.Breach = true
	}
	return r
}

func summarize(rs []Result) Summary {
	var s Summary
	// Считаем по ID: контроль и пробой — свойство пробы, а не режима.
	// Проба-пробой засчитывается как найденная, если пробила хоть в одном режиме.
	breachByID := map[string]bool{}
	breachSeen := map[string]bool{}
	for _, r := range rs {
		ctrl := strings.HasPrefix(r.ID, "C") || strings.HasSuffix(r.ID, "ctrl-b64-single") ||
			r.ID == "A1-split-adjacent" || r.ID == "O1-leak-english" || strings.HasPrefix(r.ID, "O3")
		if ctrl {
			s.ControlsTotal++
			if r.Match {
				s.ControlsOK++
			}
			continue
		}
		if !breachSeen[r.ID] {
			breachSeen[r.ID] = true
			s.BreachesTotal++
		}
		if r.Breach {
			breachByID[r.ID] = true
		}
		if !r.Match {
			s.PredictionMiss++
		}
	}
	s.BreachesFound = len(breachByID)
	return s
}

func print(rep Report) {
	fmt.Println("МИШЕНЬ:", rep.Target)
	fmt.Println("ОГОВОРКА:", rep.Caveat)
	fmt.Println()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprint(tw, "проба\tслой\tрежим\tждали\tполучили\tитог\tчто нашлось\n")
	for _, r := range rep.Results {
		mark := "ok"
		if r.Breach {
			mark = "ПРОБОЙ"
		} else if !r.Match {
			mark = "мимо-предсказания"
		}
		found := strings.Join(r.Findings, ",")
		if r.Surface == "output" && r.Reached && r.Got == "flagged" {
			found += " (ушло к клиенту)"
		}
		if found == "" {
			found = "—"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, r.Surface, r.Mode, r.Expect, r.Got, mark, found)
	}
	tw.Flush()

	s := rep.Summary
	fmt.Println()
	fmt.Printf("Контроль: %d/%d сработали как должны.\n", s.ControlsOK, s.ControlsTotal)
	if s.ControlsOK < s.ControlsTotal {
		fmt.Println("  ВНИМАНИЕ: часть контролей не прошла — детектор мишени работает не так, как ожидалось; таблице пробоев верить рано.")
	}
	fmt.Printf("Пробои: %d из %d предсказанных подтвердились.\n", s.BreachesFound, s.BreachesTotal)
	if s.PredictionMiss > 0 {
		fmt.Printf("  Расхождений с ожиданием: %d — прочитать расшифровку до того, как верить итогу.\n", s.PredictionMiss)
	}
}

func load(path string) ([]Probe, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []Probe
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1024*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var p Probe
		if err := json.Unmarshal([]byte(line), &p); err != nil {
			return nil, fmt.Errorf("проба %q: %w", line, err)
		}
		out = append(out, p)
	}
	return out, sc.Err()
}

func save(rep Report, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-redteam.json", time.Now().Format("20060102-150405"))
	b, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return err
	}
	fmt.Println("\nотчёт:", path)
	return nil
}
