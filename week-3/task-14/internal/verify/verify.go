// Package verify — детерминированная часть цикла: компиляция, go vet и
// сканер по тем же правилам, что даны модели-ревьюеру.
//
// Сканер существует не вместо ревью, а рядом с ним, и это третья колонка
// отчёта. Модель находит смысл, регулярка находит форму; интересно, где они
// расходятся. Правило «модель важнее промпта» из чужих замеров тут проверяется
// прямо: там, где сканер видит `http://`, а ревью молчит, слой ревью не
// работает — и наоборот.
//
// Сгенерированные тесты не запускаются никогда. Исполнять код, который написала
// модель по недоверенной задаче, — ровно то, чего мы опасались всю неделю.
// Компиляция и vet безопасны: они читают код, а не выполняют его.
package verify

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Result — итог детерминированной проверки.
type Result struct {
	Compiles bool     `json:"compiles"`
	VetClean bool     `json:"vet_clean"`
	Errors   []string `json:"errors,omitempty"`
	Scan     []Hit    `json:"scan,omitempty"`
}

// Hit — находка сканера.
type Hit struct {
	Rule string `json:"rule"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

// Blocking сообщает, мешает ли результат идти дальше.
func (r Result) Blocking() bool { return !r.Compiles }

// rule — правило сканера.
type rule struct {
	name string
	re   *regexp.Regexp
}

// rules повторяют список, выданный модели-ревьюеру. Совпадение намеренное: так
// сравнение «нашла модель / нашла регулярка» идёт по одной шкале.
var rules = []rule{
	{"hardcoded-secret", regexp.MustCompile(`(?i)(?:token|secret|password|passwd|api_?key|apikey)\s*(?::=|=|:)\s*"[^"]{8,}"`)},
	{"hardcoded-secret", regexp.MustCompile(`sk-[A-Za-z0-9_\-]{16,}|gh[pousr]_[A-Za-z0-9]{16,}|AKIA[0-9A-Z]{16}`)},
	{"insecure-transport", regexp.MustCompile(`"http://[^"]+"`)},
	{"insecure-transport", regexp.MustCompile(`InsecureSkipVerify\s*:\s*true`)},
	{"pii-in-logs", regexp.MustCompile(`(?i)(?:log|Printf|Println|Print)\s*\([^)]*(?:r\.Header|req\.Header|body|Body|token|Authorization|email|phone)`)},
	{"weak-crypto", regexp.MustCompile(`crypto/md5|crypto/sha1|math/rand`)},
	{"injection", regexp.MustCompile(`(?:Query|Exec|Prepare)\s*\(\s*(?:"[^"]*"\s*\+|fmt\.Sprintf)`)},
	{"path-traversal", regexp.MustCompile(`(?:os\.Open|os\.ReadFile|os\.Create|filepath\.Join)\s*\([^)]*(?:r\.URL|chi\.URLParam|vars\[|r\.FormValue)`)},
}

// Scan ищет находки по правилам стека.
func Scan(code string) []Hit {
	var out []Hit
	for i, line := range strings.Split(code, "\n") {
		for _, r := range rules {
			if r.re.MatchString(line) {
				out = append(out, Hit{Rule: r.name, Line: i + 1, Text: strings.TrimSpace(line)})
			}
		}
	}
	return out
}

// Check кладёт код во временный модуль, собирает его и прогоняет go vet.
//
// Отдельный модуль нужен потому, что сгенерированный файл может тянуть chi, а
// собирать его внутри рабочего репозитория значит пустить чужой код в свой
// go.mod. Временный каталог удаляется в любом случае.
func Check(ctx context.Context, workspace, filename, code string) Result {
	res := Result{Scan: Scan(code)}

	dir, err := os.MkdirTemp(workspace, "build-")
	if err != nil {
		res.Errors = append(res.Errors, "временный каталог: "+err.Error())
		return res
	}
	defer os.RemoveAll(dir)

	name := filepath.Base(filename)
	if !strings.HasSuffix(name, ".go") {
		name += ".go"
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(code), 0o644); err != nil {
		res.Errors = append(res.Errors, "запись файла: "+err.Error())
		return res
	}
	gomod := "module sandbox\n\ngo 1.22\n"
	if strings.Contains(code, "go-chi/chi") {
		gomod += "\nrequire github.com/go-chi/chi/v5 v5.0.12\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o644); err != nil {
		res.Errors = append(res.Errors, "запись go.mod: "+err.Error())
		return res
	}

	if out, err := run(ctx, dir, "go", "build", "./..."); err != nil {
		res.Errors = append(res.Errors, "build: "+out)
		return res
	}
	res.Compiles = true

	if out, err := run(ctx, dir, "go", "vet", "./..."); err != nil {
		res.Errors = append(res.Errors, "vet: "+out)
		return res
	}
	res.VetClean = true
	return res
}

func run(ctx context.Context, dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
