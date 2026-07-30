// Package gate — контроль принятия результата инференса.
//
// Четыре слоя, от бесплатных к дорогим:
//
//	0. проверка входа        — без обращения к модели вообще
//	1. constraint + scoring  — один вызов, вероятности приходят тем же ответом
//	2. redundancy            — несколько выборок с температурой, согласие между ними
//	3. self-check            — только для сомнительных случаев
//
// Порядок не случаен: каждый следующий слой стоит дороже предыдущего, поэтому
// вход, отвергнутый нулевым слоем, не тратит ни одного вызова, а самопроверка
// запускается только там, где первые слои разошлись.
//
// Итог — один из трёх вердиктов. OK означает «результат можно принимать
// автоматически», FAIL — «принимать нельзя», UNSURE существует только внутри
// конвейера: наружу он не выходит, потому что самопроверка обязана превратить
// его в OK или FAIL.
package gate

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/swanden/ai-challenge-advanced/week-2/task-7/internal/llm"
	"github.com/swanden/ai-challenge-advanced/week-2/task-7/internal/spec"
)

// Способы получить несколько независимых ответов на один вход.
const (
	// SampleShuffle проворачивает перечисление классов в системном промпте
	// при температуре ноль. Проверяет, не держится ли ответ на порядке
	// вариантов в списке.
	SampleShuffle = "shuffle"
	// SampleTemperature берёт выборки с температурой выше нуля.
	SampleTemperature = "temperature"
)

// Status — вердикт по одному входу.
type Status string

const (
	StatusOK     Status = "OK"
	StatusUnsure Status = "UNSURE"
	StatusFail   Status = "FAIL"
)

// Thresholds — пороги принятия. Значения зафиксированы до первого прогона,
// обоснование — в docs/thresholds.md.
type Thresholds struct {
	MinSeqProb   float64 // ниже — ответ считается неуверенным
	MinMargin    float64 // отрыв от второй по вероятности альтернативы
	Samples      int     // сколько выборок берёт слой избыточности
	SampleTemp   float64 // температура для выборок; при нуле избыточность бессмысленна
	MinAgreement int     // сколько выборок должны совпасть
	MinInputLen  int     // короче — вход отвергается без обращения к модели
	SampleMode   string  // shuffle — проворот списка классов, temperature — выборки с температурой
}

// Default возвращает пороги по умолчанию.
func Default() Thresholds {
	return Thresholds{
		MinSeqProb:   0.80,
		MinMargin:    0.30,
		Samples:      3,
		SampleTemp:   0.7,
		MinAgreement: 3,
		MinInputLen:  15,
		SampleMode:   SampleShuffle,
	}
}

// Result — что произошло на каждом слое и чем всё закончилось.
type Result struct {
	Input string `json:"input"`

	InputOK     bool   `json:"input_ok"`
	InputReason string `json:"input_reason,omitempty"`

	Raw          string  `json:"raw,omitempty"`
	Class        string  `json:"class,omitempty"`
	FormatOK     bool    `json:"format_ok"`
	FormatReason string  `json:"format_reason,omitempty"`
	SeqProb      float64 `json:"seq_prob"`
	Margin       float64 `json:"margin"`
	ScoreOK      bool    `json:"score_ok"`

	Samples        []string `json:"samples,omitempty"`
	SampleVariants []string `json:"sample_variants,omitempty"`
	Agreement int      `json:"agreement"`
	Majority  string   `json:"majority,omitempty"`
	AgreeOK   bool     `json:"agree_ok"`

	SelfCheckRan       bool   `json:"self_check_ran"`
	SelfCheckRaw       string `json:"self_check_raw,omitempty"`
	SelfCheckConfirmed bool   `json:"self_check_confirmed"`

	Status   Status `json:"status"`
	Accepted string `json:"accepted,omitempty"`
	Reason   string `json:"reason"`

	Calls            int   `json:"calls"`
	PromptTokens     int   `json:"prompt_tokens"`
	CompletionTokens int   `json:"completion_tokens"`
	LatencyMS        int64 `json:"latency_ms"`
}

// Evaluate прогоняет один вход через все слои.
func Evaluate(c *llm.Client, sp *spec.Spec, th Thresholds, input string) (Result, error) {
	r := Result{Input: input}

	// ── Слой 0: вход. Ни одного вызова модели.
	if ok, reason := checkInput(input, th.MinInputLen); !ok {
		r.InputOK = false
		r.InputReason = reason
		r.Status = StatusFail
		r.Reason = "вход отвергнут до инференса: " + reason
		return r, nil
	}
	r.InputOK = true

	// ── Слой 1: основной ответ, он же оценка по вероятностям.
	main, err := c.Ask(llm.Request{
		Messages:    classifyMessages(sp, input),
		Temperature: 0,
		MaxTokens:   16,
		LogProbs:    true,
		TopLogProbs: 8,
		Seed:        1,
	})
	r.Calls++
	r.PromptTokens += main.PromptTokens
	r.CompletionTokens += main.CompletionTokens
	r.LatencyMS += main.LatencyMS
	if err != nil {
		return r, fmt.Errorf("основной вызов: %w", err)
	}
	r.Raw = main.Raw

	class, exact := sp.ParseClass(main.Raw)
	switch {
	case class == "":
		r.FormatOK = false
		r.FormatReason = "в ответе нет ни одного известного класса"
	case !exact:
		r.FormatOK = false
		r.FormatReason = "класс пришлось извлекать из многословного ответа"
	default:
		r.FormatOK = true
	}
	r.Class = class

	if !r.FormatOK {
		r.Status = StatusFail
		r.Reason = "нарушен формат: " + r.FormatReason
		return r, nil
	}

	r.SeqProb = main.SeqProb()
	r.Margin = main.Margin()
	// Отрицательные значения означают, что вероятности не пришли. В этом
	// случае слой считается пройденным: отсутствие данных не должно
	// молча превращаться в отказ.
	r.ScoreOK = (r.SeqProb < 0 || r.SeqProb >= th.MinSeqProb) &&
		(r.Margin < 0 || r.Margin >= th.MinMargin)

	// ── Слой 2: избыточность. Разные seed при одной температуре, чтобы
	// выборки различались, но прогон оставался воспроизводимым.
	counts := map[string]int{}
	// Провороты выбраны так, чтобы последний класс списка побывал и первым:
	// при восьми классах шаги 1, 3, 5 разносят его по трём разным местам.
	rotations := []int{1, 3, 5}
	for i := 0; i < th.Samples; i++ {
		msgs := classifyMessages(sp, input)
		temp := th.SampleTemp
		seed := 101 + i
		variant := fmt.Sprintf("температура %.1f, seed %d", temp, seed)

		if th.SampleMode == SampleShuffle {
			k := rotations[i%len(rotations)] + i/len(rotations)
			prompt, ok := sp.PromptRotated(k)
			if !ok {
				return r, fmt.Errorf("выборка %d: перечисление классов в промпте не найдено, проворот невозможен", i+1)
			}
			msgs = []llm.Message{
				{Role: "system", Content: prompt},
				{Role: "user", Content: input},
			}
			temp = 0
			seed = 1
			variant = fmt.Sprintf("проворот списка на %d", k)
		}
		r.SampleVariants = append(r.SampleVariants, variant)

		s, err := c.Ask(llm.Request{
			Messages:    msgs,
			Temperature: temp,
			MaxTokens:   16,
			Seed:        seed,
		})
		r.Calls++
		r.PromptTokens += s.PromptTokens
		r.CompletionTokens += s.CompletionTokens
		r.LatencyMS += s.LatencyMS
		if err != nil {
			return r, fmt.Errorf("выборка %d: %w", i+1, err)
		}
		got, _ := sp.ParseClass(s.Raw)
		if got == "" {
			got = "?"
		}
		r.Samples = append(r.Samples, got)
		counts[got]++
	}
	r.Majority, r.Agreement = majority(counts)
	r.AgreeOK = r.Agreement >= th.MinAgreement && r.Majority == r.Class

	// ── Свод первых слоёв.
	switch {
	case r.ScoreOK && r.AgreeOK:
		r.Status = StatusOK
		r.Accepted = r.Class
		r.Reason = "оба сигнала уверенности сошлись"
		return r, nil
	case !r.ScoreOK && !r.AgreeOK:
		r.Status = StatusFail
		r.Reason = fmt.Sprintf("оба сигнала против: вероятность %.2f при пороге %.2f, отрыв %.2f при пороге %.2f, согласие %d из %d за %q",
			r.SeqProb, th.MinSeqProb, r.Margin, th.MinMargin, r.Agreement, th.Samples, r.Majority)
		return r, nil
	}
	r.Status = StatusUnsure

	// ── Слой 3: самопроверка, только для сомнительных.
	check, err := c.Ask(llm.Request{
		Messages:    selfCheckMessages(sp, input, r.Class),
		Temperature: 0,
		MaxTokens:   16,
		Seed:        1,
	})
	r.Calls++
	r.SelfCheckRan = true
	r.PromptTokens += check.PromptTokens
	r.CompletionTokens += check.CompletionTokens
	r.LatencyMS += check.LatencyMS
	if err != nil {
		return r, fmt.Errorf("самопроверка: %w", err)
	}
	r.SelfCheckRaw = check.Raw
	r.SelfCheckConfirmed = confirms(sp, check.Raw, r.Class)

	if r.SelfCheckConfirmed {
		r.Status = StatusOK
		r.Accepted = r.Class
		r.Reason = "сигналы разошлись, самопроверка подтвердила ответ"
		return r, nil
	}
	r.Status = StatusFail
	r.Reason = "сигналы разошлись, самопроверка ответ не подтвердила"
	return r, nil
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

// classifyMessages собирает запрос классификации. Промпт берётся из
// контракта, восстановленного по датасету Дня 6, — то есть буквально тот,
// каким датасет размечен.
func classifyMessages(sp *spec.Spec, input string) []llm.Message {
	return []llm.Message{
		{Role: "system", Content: sp.SystemPrompt},
		{Role: "user", Content: input},
	}
}

// selfCheckMessages собирает запрос самопроверки. Формулировка намеренно
// не подталкивает к согласию: «подтверждаю» и «правильный класс» —
// равноправные варианты ответа, и слово «ошибка» не упоминается.
func selfCheckMessages(sp *spec.Spec, input, class string) []llm.Message {
	system := "Ты проверяешь разметку задач репозитория notes-api. Допустимые классы: " +
		sp.List() +
		". Тебе дают формулировку задачи и предложенный для неё класс. " +
		"Если предложенный класс верен, ответь одним словом: подтверждаю. " +
		"Если верен другой класс, ответь его именем, одним словом."
	user := "Задача: " + input + "\nПредложенный класс: " + class
	return []llm.Message{
		{Role: "system", Content: system},
		{Role: "user", Content: user},
	}
}

// confirms разбирает ответ самопроверки.
func confirms(sp *spec.Spec, raw, class string) bool {
	s := strings.ToLower(strings.TrimSpace(raw))
	if strings.Contains(s, "подтвержд") {
		return true
	}
	got, _ := sp.ParseClass(raw)
	return got == class
}

// majority возвращает самый частый ответ и число голосов за него.
func majority(counts map[string]int) (string, int) {
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys) // стабильность при равенстве голосов
	best, n := "", 0
	for _, k := range keys {
		if counts[k] > n {
			best, n = k, counts[k]
		}
	}
	return best, n
}
