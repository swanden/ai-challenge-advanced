// Package router — маршрутизация запроса между дешёвой и дорогой моделью.
//
// Устройство простое: сначала отвечает малая модель, дальше по её ответу
// решается, доверять ему или переспросить у большой. Смысл в стоимости —
// большая модель работает не на всех входах, а только там, где малая
// споткнулась.
//
// Все признаки эскалации намеренно дешёвые. В Дне 7 сомнение обходилось
// в четыре вызова малой модели на вход, и для маршрутизации это
// бессмысленно: экономия на большой модели съедалась расходом на малую.
// Поэтому здесь три признака берутся из одного-единственного ответа
// (формат, вероятности, длина), а четвёртый вообще не требует модели.
//
// Четвёртый признак существует потому, что День 7 измерил слепое пятно:
// на классе contract-change малая модель ошибалась с вероятностью до 1.00
// и полным согласием выборок. Уверенность там ничего не показывает, значит
// эскалацию должен запускать другой механизм — разбор самой формулировки.
package router

import (
	"fmt"
	"strings"
	"unicode"

	"github.com/swanden/ai-challenge-advanced/week-2/task-8/internal/llm"
	"github.com/swanden/ai-challenge-advanced/week-2/task-8/internal/spec"
)

// Режимы работы. Нужны, чтобы в одном прогоне сравнить маршрутизацию
// с двумя крайностями: «всё на малой» и «всё на большой».
const (
	ModeSmall = "small" // только малая модель, эскалации нет
	ModeRoute = "route" // малая, с эскалацией по признакам
	ModeBig   = "big"   // только большая модель, потолок качества
)

// Policy — правила эскалации. Пороги записаны до прогона, обоснование
// в docs/routing.md.
type Policy struct {
	Mode            string
	MinSeqProb      float64 // ниже — ответ считается неуверенным
	MinMargin       float64 // отрыв от ближайшей альтернативы
	MaxAnswerTokens int     // длиннее — модель начала рассуждать вместо ответа
	MinInputLen     int     // короче — вход отвергается без обращения к модели
	UseMarkers      bool    // включать разбор формулировки на признаки контракта
}

// Default возвращает правила по умолчанию.
func Default() Policy {
	return Policy{
		Mode:            ModeRoute,
		MinSeqProb:      0.80,
		MinMargin:       0.30,
		MaxAnswerTokens: 5,
		MinInputLen:     15,
		UseMarkers:      true,
	}
}

// Attempt — одно обращение к модели.
type Attempt struct {
	Model            string  `json:"model"`
	Raw              string  `json:"raw"`
	Class            string  `json:"class"`
	FormatOK         bool    `json:"format_ok"`
	SeqProb          float64 `json:"seq_prob"`
	Margin           float64 `json:"margin"`
	LatencyMS        int64   `json:"latency_ms"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
}

// Decision — что произошло с одним входом.
type Decision struct {
	Input       string   `json:"input"`
	InputOK     bool     `json:"input_ok"`
	InputReason string   `json:"input_reason,omitempty"`
	Small       *Attempt `json:"small,omitempty"`
	Big         *Attempt `json:"big,omitempty"`
	Escalated   bool     `json:"escalated"`
	Triggers    []string `json:"triggers,omitempty"`
	Final       string   `json:"final,omitempty"`
	DecidedBy   string   `json:"decided_by"` // small, big или none

	Calls            int   `json:"calls"`
	BigCalls         int   `json:"big_calls"`
	PromptTokens     int   `json:"prompt_tokens"`
	CompletionTokens int   `json:"completion_tokens"`
	LatencyMS        int64 `json:"latency_ms"`
}

// Route проводит один вход через маршрутизатор.
func Route(small, big *llm.Client, sp *spec.Spec, pol Policy, input string) (Decision, error) {
	d := Decision{Input: input, DecidedBy: "none"}

	// Негодный вход отвергается без единого вызова — самая дешёвая
	// ступень из возможных.
	if ok, reason := checkInput(input, pol.MinInputLen); !ok {
		d.InputReason = reason
		return d, nil
	}
	d.InputOK = true

	// Режим «только большая» существует ради потолка: он показывает,
	// чего маршрутизация могла бы достичь в пределе, и во что это
	// обошлось бы.
	if pol.Mode == ModeBig {
		a, err := ask(big, sp, input)
		d.Calls++
		d.BigCalls++
		accrue(&d, a)
		if err != nil {
			return d, fmt.Errorf("большая модель: %w", err)
		}
		d.Big = a
		d.Final = a.Class
		d.DecidedBy = "big"
		return d, nil
	}

	a, err := ask(small, sp, input)
	d.Calls++
	accrue(&d, a)
	if err != nil {
		return d, fmt.Errorf("малая модель: %w", err)
	}
	d.Small = a
	d.Final = a.Class
	d.DecidedBy = "small"

	d.Triggers = triggers(a, pol, input)
	if pol.Mode == ModeSmall || len(d.Triggers) == 0 {
		return d, nil
	}

	// Эскалация. Ответ большой модели считается окончательным: если бы
	// мы стали сравнивать его с ответом малой и выбирать, понадобился бы
	// третий арбитр, а его нет.
	b, err := ask(big, sp, input)
	d.Calls++
	d.BigCalls++
	accrue(&d, b)
	if err != nil {
		return d, fmt.Errorf("эскалация: %w", err)
	}
	d.Big = b
	d.Escalated = true
	d.Final = b.Class
	d.DecidedBy = "big"
	return d, nil
}

// triggers перечисляет сработавшие признаки эскалации. Все они считаются
// по уже полученному ответу либо по самой формулировке, поэтому не стоят
// ни одного дополнительного вызова.
func triggers(a *Attempt, pol Policy, input string) []string {
	var out []string
	if !a.FormatOK {
		out = append(out, "формат")
	}
	if a.SeqProb >= 0 && a.SeqProb < pol.MinSeqProb {
		out = append(out, fmt.Sprintf("вероятность %.2f", a.SeqProb))
	}
	if a.Margin >= 0 && a.Margin < pol.MinMargin {
		out = append(out, fmt.Sprintf("отрыв %.2f", a.Margin))
	}
	if pol.MaxAnswerTokens > 0 && a.CompletionTokens > pol.MaxAnswerTokens {
		out = append(out, fmt.Sprintf("длина ответа %d токенов", a.CompletionTokens))
	}
	if pol.UseMarkers && ContractRisk(input) {
		out = append(out, "признаки смены контракта")
	}
	return out
}

// Слова, по которым распознаётся разговор о публичном контракте.
// Список написан руками, из головы, и это его главный недостаток:
// он неизбежно и пропускает, и срабатывает зря. Насколько именно —
// меряется на прогоне и пишется в отчёт.
var contractNouns = []string{
	"сигнатур", "интерфейс", "параметр", "поле", "статус",
	"маршрут", "эндпоинт", "endpoint", "путь", "ответ", "код",
}

var changeVerbs = []string{
	"переименов", "перенес", "перенест", "измен", "поменя",
	"убрать", "убери", "удали", "заменить", "замени",
	"должен принимать", "должен возвращать", "вместо",
}

// ContractRisk сообщает, похоже ли, что выполнение задачи затронет
// публичный контракт.
//
// Требуется совпадение из обеих групп сразу: одно существительное про
// контракт и один глагол изменения. Поодиночке они срабатывают слишком
// часто — слово «ответ» встречается едва ли не в каждой второй
// формулировке.
func ContractRisk(input string) bool {
	s := strings.ToLower(input)
	noun, verb := false, false
	for _, n := range contractNouns {
		if strings.Contains(s, n) {
			noun = true
			break
		}
	}
	for _, v := range changeVerbs {
		if strings.Contains(s, v) {
			verb = true
			break
		}
	}
	return noun && verb
}

// ask задаёт вопрос одной модели и разбирает ответ.
func ask(c *llm.Client, sp *spec.Spec, input string) (*Attempt, error) {
	a := &Attempt{Model: c.Model}
	ans, err := c.Ask(llm.Request{
		Messages: []llm.Message{
			{Role: "system", Content: sp.SystemPrompt},
			{Role: "user", Content: input},
		},
		Temperature: 0,
		MaxTokens:   16,
		LogProbs:    true,
		TopLogProbs: 8,
		Seed:        1,
	})
	a.LatencyMS = ans.LatencyMS
	a.PromptTokens = ans.PromptTokens
	a.CompletionTokens = ans.CompletionTokens
	if err != nil {
		return a, err
	}
	a.Raw = ans.Raw
	class, exact := sp.ParseClass(ans.Raw)
	a.Class = class
	a.FormatOK = exact
	a.SeqProb = ans.SeqProb()
	a.Margin = ans.Margin()
	return a, nil
}

func accrue(d *Decision, a *Attempt) {
	if a == nil {
		return
	}
	d.PromptTokens += a.PromptTokens
	d.CompletionTokens += a.CompletionTokens
	d.LatencyMS += a.LatencyMS
}

// checkInput отсекает то, на что незачем тратить инференс.
func checkInput(s string, minLen int) (bool, string) {
	t := strings.TrimSpace(s)
	if t == "" {
		return false, "пустая формулировка"
	}
	runes := []rune(t)
	if len(runes) < minLen {
		return false, fmt.Sprintf("формулировка короче %d символов", minLen)
	}
	letters := 0
	for _, r := range runes {
		if unicode.IsLetter(r) {
			letters++
		}
	}
	if float64(letters)/float64(len(runes)) < 0.5 {
		return false, "меньше половины символов — буквы, похоже на мусор"
	}
	return true, ""
}
