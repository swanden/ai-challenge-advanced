// Команда validate проверяет датасет перед тем, как отдавать его на обучение.
//
// Проверки закрывают требования задания (каждая строка — валидный JSON,
// все три роли на месте, пустых content нет) и добавляют то, без чего
// датасет тихо испортит обучение: дубли, перекрытие train и eval,
// перекос классов, слишком короткие и слишком длинные формулировки,
// доля реальных данных.
//
// Выход: 0 — годен, 1 — есть ошибки. Предупреждения на код возврата не влияют.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/swanden/ai-challenge-advanced/week-2/task-6/internal/dataset"
)

const (
	minUserLen = 15  // короче — формулировка ни о чём
	maxUserLen = 400 // длиннее — это уже не формулировка задачи
	minReal    = 0.20
)

type report struct {
	errs  []string
	warns []string
}

func (r *report) errf(format string, args ...any) {
	r.errs = append(r.errs, fmt.Sprintf(format, args...))
}

func (r *report) warnf(format string, args ...any) {
	r.warns = append(r.warns, fmt.Sprintf(format, args...))
}

func main() {
	masterPath := flag.String("master", "week-2/task-6/dataset/master.jsonl", "путь к master.jsonl")
	trainPath := flag.String("train", "week-2/task-6/dataset/train.jsonl", "путь к train.jsonl")
	evalPath := flag.String("eval", "week-2/task-6/dataset/eval.jsonl", "путь к eval.jsonl")
	flag.Parse()

	rep := &report{}

	train := checkFile(rep, *trainPath)
	eval := checkFile(rep, *evalPath)

	checkSplit(rep, len(train), len(eval))
	checkDuplicates(rep, train, eval)
	checkBalance(rep, "train", train)
	checkBalance(rep, "eval", eval)
	checkMaster(rep, *masterPath, train, eval)

	fmt.Printf("train %d примеров, eval %d примеров\n", len(train), len(eval))
	printDistribution("train", train)
	printDistribution("eval", eval)

	for _, w := range rep.warns {
		fmt.Printf("предупреждение: %s\n", w)
	}
	if len(rep.errs) == 0 {
		fmt.Println("датасет годен")
		return
	}
	for _, e := range rep.errs {
		fmt.Fprintf(os.Stderr, "ошибка: %s\n", e)
	}
	fmt.Fprintf(os.Stderr, "всего ошибок: %d\n", len(rep.errs))
	os.Exit(1)
}

// checkFile читает файл построчно, чтобы номер строки попал в сообщение.
func checkFile(rep *report, path string) []dataset.Example {
	lines, err := dataset.ReadLines(path)
	if err != nil {
		rep.errf("%s: не прочитан: %v", path, err)
		return nil
	}
	if len(lines) == 0 {
		rep.errf("%s: пустой файл", path)
		return nil
	}
	out := make([]dataset.Example, 0, len(lines))
	for _, ln := range lines {
		ex, err := dataset.ParseExample(ln.Raw)
		if err != nil {
			rep.errf("%s:%d: %v", path, ln.No, err)
			continue
		}
		user := ex.Messages[1].Content
		switch n := len([]rune(user)); {
		case n < minUserLen:
			rep.errf("%s:%d: формулировка короче %d символов (%d)", path, ln.No, minUserLen, n)
		case n > maxUserLen:
			rep.errf("%s:%d: формулировка длиннее %d символов (%d)", path, ln.No, maxUserLen, n)
		}
		out = append(out, ex)
	}
	return out
}

func checkSplit(rep *report, train, eval int) {
	total := train + eval
	if total == 0 {
		return
	}
	if total < 50 {
		rep.errf("в датасете %d примеров, задание требует минимум 50", total)
	}
	share := float64(eval) / float64(total)
	if share < 0.15 || share > 0.25 {
		rep.warnf("доля eval %.0f%% заметно отличается от 20%%", share*100)
	}
}

func checkDuplicates(rep *report, train, eval []dataset.Example) {
	seen := map[string]string{} // нормализованный текст -> где встретился
	add := func(name string, exs []dataset.Example) {
		for i, ex := range exs {
			key := dataset.Normalize(ex.Messages[1].Content)
			where := fmt.Sprintf("%s[%d]", name, i+1)
			if prev, ok := seen[key]; ok {
				rep.errf("дубль формулировки: %s и %s", prev, where)
				continue
			}
			seen[key] = where
		}
	}
	add("train", train)
	add("eval", eval)
}

func checkBalance(rep *report, name string, exs []dataset.Example) {
	if len(exs) == 0 {
		return
	}
	counts := distribution(exs)
	for _, cls := range dataset.SortedClasses() {
		if counts[cls] == 0 {
			rep.errf("%s: класс %s не представлен ни одним примером", name, cls)
		}
	}
	min, max := -1, 0
	for _, cls := range dataset.SortedClasses() {
		n := counts[cls]
		if min < 0 || n < min {
			min = n
		}
		if n > max {
			max = n
		}
	}
	if min > 0 && max > 2*min {
		rep.warnf("%s: перекос классов, самый частый встречается в %.1f раза чаще самого редкого",
			name, float64(max)/float64(min))
	}
}

func checkMaster(rep *report, path string, train, eval []dataset.Example) {
	recs, err := dataset.LoadMaster(path)
	if err != nil {
		rep.errf("%s: не прочитан: %v", path, err)
		return
	}
	if n := len(train) + len(eval); n != len(recs) {
		rep.errf("в master %d записей, а в train+eval %d — файлы разошлись, пересоберите build", len(recs), n)
	}

	real := 0
	byText := map[string]dataset.Record{}
	for _, r := range recs {
		if r.Real {
			real++
			if strings.TrimSpace(r.Origin) == "" {
				rep.errf("%s: помечен реальным, но происхождение не указано", r.ID)
			}
		}
		key := dataset.Normalize(r.User)
		if prev, ok := byText[key]; ok {
			rep.errf("master: %s и %s — одна и та же формулировка", prev.ID, r.ID)
		}
		byText[key] = r
	}
	share := float64(real) / float64(len(recs))
	if share < minReal {
		rep.errf("реальных примеров %.0f%%, задание требует минимум %.0f%%", share*100, minReal*100)
	}
	fmt.Printf("реальных примеров %d из %d (%.0f%%)\n", real, len(recs), share*100)

	// Каждая формулировка из train и eval должна находиться в master:
	// иначе провенанс датасета неполный.
	for _, ex := range append(append([]dataset.Example{}, train...), eval...) {
		key := dataset.Normalize(ex.Messages[1].Content)
		if _, ok := byText[key]; !ok {
			rep.errf("формулировка отсутствует в master: %.60s...", ex.Messages[1].Content)
		}
	}
}

func distribution(exs []dataset.Example) map[string]int {
	counts := map[string]int{}
	for _, ex := range exs {
		counts[ex.Messages[2].Content]++
	}
	return counts
}

func printDistribution(name string, exs []dataset.Example) {
	if len(exs) == 0 {
		return
	}
	counts := distribution(exs)
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, counts[k]))
	}
	fmt.Printf("%s: %s\n", name, strings.Join(parts, ", "))
}
