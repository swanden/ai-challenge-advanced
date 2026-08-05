// Package report собирает записи прогона и считает по ним метрики.
//
// Главное здесь — вторая метрика. Доля успешных атак сама по себе ничего не
// говорит: механизм, отвергающий вообще всё, покажет ноль успешных атак и
// будет бесполезен. Поэтому рядом с ней всегда идёт цена защиты — что слои
// сломали на чистых входах, где никакой атаки нет.
//
// Приём взят из Дня 7: там в набор проб намеренно вложены два трудных, но
// осмысленных входа, и без них любая строгость выглядела бы идеальной. Здесь
// ту же роль играют шестнадцать примеров eval Дня 6 и восемь честных вопросов
// к банку.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
)

// Виды записей.
const (
	KindAttack = "attack"
	KindClean  = "clean"
)

// Исходы чистого прогона.
const (
	CleanOK      = "ok"      // мишень отработала как должна
	CleanWrong   = "wrong"   // валидный ответ, но неверный по существу
	CleanBroken  = "broken"  // формат ответа сорван
	CleanBlocked = "blocked" // вход срезан входным фильтром
	CleanRefused = "refused" // мишень отказалась отвечать на честный вопрос
)

// Record — одна попытка.
type Record struct {
	Target    string `json:"target"`
	Defense   string `json:"defense"`
	Kind      string `json:"kind"`
	ID        string `json:"id"`
	Technique string `json:"technique,omitempty"`
	Type      string `json:"type,omitempty"`
	Repeat    int    `json:"repeat"`
	Outcome   string `json:"outcome"`
	BlockedBy string `json:"blocked_by,omitempty"`
	GuardHit  bool   `json:"guard_hit,omitempty"`
	Output    string `json:"output"`
	// Отправленное в модель. В JSON не попадает: системный промпт одинаков
	// для сотен записей, и файл распух бы вчетверо. Нужно только для показа
	// диалога флагами -show-all и -show-hits.
	SentSystem string `json:"-"`
	SentUser   string `json:"-"`
	LatencyMs  int64  `json:"latency_ms"`
	Err        string `json:"error,omitempty"`
}

// Meta — обстоятельства прогона.
type Meta struct {
	StartedAt    string  `json:"started_at"`
	Provider     string  `json:"provider"`
	Model        string  `json:"model"`
	Temperature  string  `json:"temperature"`
	MinCalls     float64 `json:"min_calls"`
	LeakScope    string  `json:"leak_scope"`
	Repeats      int     `json:"repeats"`
	SystemInUser bool    `json:"system_in_user"`
	AttacksFile  string  `json:"attacks_file"`
	CleanFile    string  `json:"clean_file"`
	EvalFile     string  `json:"eval_file"`
	Note         string  `json:"note"`
}

// Report — всё, что осталось от прогона.
type Report struct {
	Meta    Meta     `json:"meta"`
	Records []Record `json:"records"`
}

// New заводит пустой отчёт.
func New(m Meta) *Report {
	m.StartedAt = time.Now().Format(time.RFC3339)
	return &Report{Meta: m}
}

// Add добавляет запись.
func (r *Report) Add(rec Record) { r.Records = append(r.Records, rec) }

// Sort приводит записи к воспроизводимому порядку. Прогон идёт в несколько
// потоков, поэтому без сортировки два одинаковых запуска дали бы файлы,
// различающиеся только порядком строк.
func (r *Report) Sort() {
	sort.SliceStable(r.Records, func(i, j int) bool {
		a, b := r.Records[i], r.Records[j]
		if a.Target != b.Target {
			return a.Target < b.Target
		}
		if a.Defense != b.Defense {
			return a.Defense < b.Defense
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		if a.ID != b.ID {
			return a.ID < b.ID
		}
		return a.Repeat < b.Repeat
	})
}

// Save пишет отчёт в каталог и возвращает путь.
func (r *Report) Save(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("%s-%s-%s.json",
		time.Now().Format("20060102-150405"),
		safe(r.Meta.Provider),
		safe(r.Meta.Model),
	)
	path := filepath.Join(dir, name)
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// counter — успехи из попыток.
type counter struct {
	hit   int
	total int
}

func (c counter) share() float64 {
	if c.total == 0 {
		return 0
	}
	return float64(c.hit) / float64(c.total) * 100
}

func (c counter) String() string {
	if c.total == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%% (%d/%d)", c.share(), c.hit, c.total)
}

// Valid сообщает, состоялся ли замер: какая доля попыток дошла до модели и не
// упала. Второй результат — текст первой ошибки, если она была.
//
// Метод появился после прогона, в котором все 708 вызовов упали на отвергнутом
// поле запроса, а таблицы напечатались как ни в чём не бывало: сплошные нули
// выглядели ответом «атаки не прошли», хотя означали «замер не состоялся».
// Печатать правдоподобные цифры поверх несостоявшегося прогона — худшее, что
// может делать измерительный инструмент.
func (r *Report) Valid(minShare float64) (bool, float64, string) {
	var done, errs int
	first := ""
	for _, rec := range r.Records {
		if rec.Err != "" {
			errs++
			if first == "" {
				first = rec.Err
			}
			continue
		}
		done++
	}
	total := done + errs
	if total == 0 {
		return false, 0, "ни одной попытки"
	}
	share := float64(done) / float64(total)
	return share >= minShare, share, first
}

// Print выводит таблицы.
func (r *Report) Print(w io.Writer, successful func(string) bool) {
	if ok, share, first := r.Valid(r.Meta.MinCalls); !ok {
		fmt.Fprintf(w, "\n%s\n", strings.Repeat("=", 60))
		fmt.Fprintf(w, "ЗАМЕР НЕДЕЙСТВИТЕЛЕН: до модели дошло %.0f%% попыток при пороге %.0f%%.\n",
			share*100, r.Meta.MinCalls*100)
		fmt.Fprintf(w, "Таблицы не печатаются: нули в них означали бы не «атаки не прошли»,\n")
		fmt.Fprintf(w, "а «замер не состоялся», и отличить одно от другого по ним нельзя.\n")
		if first != "" {
			fmt.Fprintf(w, "\nПервая ошибка:\n  %s\n", first)
		}
		fmt.Fprintf(w, "%s\n", strings.Repeat("=", 60))
		return
	}
	fmt.Fprintf(w, "\nМодель: %s (%s), температура %s, повторов %d",
		r.Meta.Model, r.Meta.Provider, r.Meta.Temperature, r.Meta.Repeats)
	if r.Meta.LeakScope != "" {
		fmt.Fprintf(w, ", утечка считается по %s", r.Meta.LeakScope)
	}
	if r.Meta.SystemInUser {
		fmt.Fprint(w, ", правила в пользовательском сообщении")
	}
	if r.Meta.Note != "" {
		fmt.Fprintf(w, "\nПометка: %s", r.Meta.Note)
	}
	fmt.Fprint(w, "\n")

	targets := r.values(func(rec Record) string { return rec.Target })
	defenses := r.orderedDefenses()

	// ---- доля успешных атак по слоям защиты
	fmt.Fprintf(w, "\nДоля успешных атак\n")
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "мишень\t%s\n", strings.Join(defenses, "\t"))
	for _, t := range targets {
		fmt.Fprintf(tw, "%s", t)
		for _, d := range defenses {
			c := counter{}
			for _, rec := range r.Records {
				if rec.Kind != KindAttack || rec.Target != t || rec.Defense != d || rec.Err != "" {
					continue
				}
				c.total++
				if successful(rec.Outcome) {
					c.hit++
				}
			}
			fmt.Fprintf(tw, "\t%s", c)
		}
		fmt.Fprint(tw, "\n")
	}
	tw.Flush()

	// ---- по техникам
	fmt.Fprintf(w, "\nДоля успешных атак по технике\n")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "мишень\tтехника\t%s\n", strings.Join(defenses, "\t"))
	for _, t := range targets {
		techs := r.values(func(rec Record) string {
			if rec.Kind == KindAttack && rec.Target == t {
				return rec.Technique
			}
			return ""
		})
		for _, tech := range techs {
			fmt.Fprintf(tw, "%s\t%s", t, tech)
			for _, d := range defenses {
				c := counter{}
				for _, rec := range r.Records {
					if rec.Kind != KindAttack || rec.Target != t || rec.Defense != d ||
						rec.Technique != tech || rec.Err != "" {
						continue
					}
					c.total++
					if successful(rec.Outcome) {
						c.hit++
					}
				}
				fmt.Fprintf(tw, "\t%s", c)
			}
			fmt.Fprint(tw, "\n")
		}
	}
	tw.Flush()

	// ---- цена защиты
	fmt.Fprintf(w, "\nЦена защиты: что сломалось на чистых входах\n")
	tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "мишень\tслой\tотработало\tсрезано фильтром\tотказ\tневерно\tсорван формат\n")
	for _, t := range targets {
		for _, d := range defenses {
			var ok, blocked, refused, wrong, broken, total int
			for _, rec := range r.Records {
				if rec.Kind != KindClean || rec.Target != t || rec.Defense != d || rec.Err != "" {
					continue
				}
				total++
				switch rec.Outcome {
				case CleanOK:
					ok++
				case CleanBlocked:
					blocked++
				case CleanRefused:
					refused++
				case CleanWrong:
					wrong++
				case CleanBroken:
					broken++
				}
			}
			if total == 0 {
				continue
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\t%d\n",
				t, d, counter{hit: ok, total: total}, blocked, refused, wrong, broken)
		}
	}
	tw.Flush()

	// ---- сработавшие атаки поимённо
	fmt.Fprintf(w, "\nСработавшие атаки (кандидаты на перепроверку десятью повторами)\n")
	type key struct{ target, defense, id, technique string }
	agg := map[key]*counter{}
	for _, rec := range r.Records {
		if rec.Kind != KindAttack || rec.Err != "" {
			continue
		}
		k := key{rec.Target, rec.Defense, rec.ID, rec.Technique}
		if agg[k] == nil {
			agg[k] = &counter{}
		}
		agg[k].total++
		if successful(rec.Outcome) {
			agg[k].hit++
		}
	}
	var keys []key
	for k, c := range agg {
		if c.hit > 0 {
			keys = append(keys, k)
		}
	}
	if len(keys) == 0 {
		fmt.Fprintln(w, "  ни одной")
	} else {
		sort.Slice(keys, func(i, j int) bool {
			if agg[keys[i]].hit != agg[keys[j]].hit {
				return agg[keys[i]].hit > agg[keys[j]].hit
			}
			return keys[i].id < keys[j].id
		})
		tw = tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		fmt.Fprintf(tw, "мишень\tслой\tатака\tтехника\tсработала\n")
		for _, k := range keys {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", k.target, k.defense, k.id, k.technique, *agg[k])
		}
		tw.Flush()
	}

	// ---- ошибки вызовов
	errs := 0
	for _, rec := range r.Records {
		if rec.Err != "" {
			errs++
		}
	}
	if errs > 0 {
		fmt.Fprintf(w, "\nОшибок вызова: %d из %d — эти попытки исключены из всех долей выше.\n",
			errs, len(r.Records))
	}
}

// orderedDefenses возвращает слои в порядке усиления, а не алфавита.
func (r *Report) orderedDefenses() []string {
	order := []string{"none", "hardened", "delimiters", "all"}
	present := map[string]bool{}
	for _, rec := range r.Records {
		present[rec.Defense] = true
	}
	var out []string
	for _, d := range order {
		if present[d] {
			out = append(out, d)
		}
	}
	for d := range present {
		if !containsStr(out, d) {
			out = append(out, d)
		}
	}
	return out
}

func (r *Report) values(pick func(Record) string) []string {
	seen := map[string]bool{}
	var out []string
	for _, rec := range r.Records {
		v := pick(rec)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func safe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '/' || r == ':' || r == ' ' {
			return '-'
		}
		return r
	}, s)
}
