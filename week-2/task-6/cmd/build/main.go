// Команда build собирает train.jsonl и eval.jsonl из master.jsonl.
//
// Разбиение детерминированное и без генератора случайных чисел: внутри
// каждого класса примеры сортируются по идентификатору, и в eval уходят
// третий и восьмой. Это даёт ровно 80/20, одинаковое число примеров
// каждого класса в eval и повторяемость — прогон на другой машине даёт
// побайтово те же файлы.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/swanden/ai-challenge-advanced/week-2/task-6/internal/dataset"
)

// evalIndexes — позиции внутри класса (с нуля), уходящие в eval.
var evalIndexes = map[int]bool{2: true, 7: true}

func main() {
	master := flag.String("master", "week-2/task-6/dataset/master.jsonl", "путь к master.jsonl")
	outDir := flag.String("out", "week-2/task-6/dataset", "каталог для train.jsonl и eval.jsonl")
	flag.Parse()

	recs, err := dataset.LoadMaster(*master)
	if err != nil {
		fail("не прочитал master: %v", err)
	}

	byClass := map[string][]dataset.Record{}
	for _, r := range recs {
		if !dataset.IsClass(r.Class) {
			fail("запись %s: класс %q вне допустимого множества", r.ID, r.Class)
		}
		byClass[r.Class] = append(byClass[r.Class], r)
	}

	var train, eval []dataset.Record
	for _, cls := range dataset.SortedClasses() {
		items := byClass[cls]
		if len(items) == 0 {
			fail("класс %s пуст", cls)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
		for i, r := range items {
			if evalIndexes[i] {
				eval = append(eval, r)
			} else {
				train = append(train, r)
			}
		}
	}

	if err := write(filepath.Join(*outDir, "train.jsonl"), train); err != nil {
		fail("%v", err)
	}
	if err := write(filepath.Join(*outDir, "eval.jsonl"), eval); err != nil {
		fail("%v", err)
	}

	total := len(train) + len(eval)
	realCount := 0
	for _, r := range recs {
		if r.Real {
			realCount++
		}
	}
	fmt.Printf("всего %d, train %d, eval %d (%.0f%% в eval)\n",
		total, len(train), len(eval), 100*float64(len(eval))/float64(total))
	fmt.Printf("реальных примеров %d (%.0f%%)\n", realCount, 100*float64(realCount)/float64(total))
}

func write(path string, recs []dataset.Record) error {
	sort.Slice(recs, func(i, j int) bool { return recs[i].ID < recs[j].ID })
	var b strings.Builder
	// SetEscapeHTML(false) обязателен: по умолчанию encoding/json
	// превращает <, > и & в \u003c и подобное, и файл перестаёт
	// побайтово совпадать с тем, что ждёт валидатор.
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, r := range recs {
		if err := enc.Encode(dataset.NewExample(r.User, r.Class)); err != nil {
			return fmt.Errorf("%s: %w", r.ID, err)
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "build: "+format+"\n", args...)
	os.Exit(1)
}
