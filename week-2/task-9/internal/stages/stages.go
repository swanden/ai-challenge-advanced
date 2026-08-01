// Package stages — два способа получить класс задачи: одним запросом и тремя.
//
// Монолитный вариант — тот самый классификатор из Дня 6: системный промпт со
// списком из восьми классов, формулировка, ответ одним словом. Он здесь не для
// красоты симметрии, а как единственная честная точка отсчёта: любая цифра
// многоэтапной схемы бессмысленна, если её не с чем сравнить.
//
// Многоэтапный вариант разбит так:
//
//	этап 1 — нормализация: из формулировки извлекаются признаки строгого вида
//	этап 2 — решение: сначала бинарный вопрос про контракт, затем выбор класса
//	этап 3 — сборка: проверка согласованности признаков с выбранным классом
//
// Разбивка не произвольная. День 8 показал, что распознавание contract-change
// рукописным словарём ловит один случай из двух: словарь не знает слова
// «заголовок» и не видит кодов статусов, записанных цифрами. Второй этап
// заменяет словарь коротким вопросом к модели — и это главное, что здесь
// проверяется.
//
// Почему отдельный вопрос вообще должен работать лучше. В монолитном запросе
// contract-change конкурирует с семью другими вариантами, и модель выбирает
// ближайший знакомый — feature или refactor. Бинарный вопрос убирает
// конкуренцию: остаются два ответа, и вероятность распределяется между ними,
// а не размазывается по восьми. Вдобавок вопрос можно задать содержательно,
// перечислив, что считается контрактом, — в общем промпте так не сделать, не
// подсказав заодно и все остальные классы.
package stages

import (
	"fmt"
	"sort"
	"strings"

	"github.com/swanden/ai-challenge-advanced/week-2/task-9/internal/llm"
	"github.com/swanden/ai-challenge-advanced/week-2/task-9/internal/spec"
)

// Models — какая модель обслуживает какой этап. Пустое значение означает
// «та же, что на предыдущем этапе».
type Models struct {
	Normalize string
	Decide    string
	Assemble  string
	Monolith  string
}

// Clients — готовые клиенты по этапам.
type Clients struct {
	Normalize *llm.Client
	Decide    *llm.Client
	Assemble  *llm.Client
	Monolith  *llm.Client
}

// Call — одно обращение к модели с тем, что нужно для отчёта.
type Call struct {
	Stage            string  `json:"stage"`
	Model            string  `json:"model"`
	Raw              string  `json:"raw"`
	SeqProb          float64 `json:"seq_prob"`
	Margin           float64 `json:"margin"`
	LatencyMS        int64   `json:"latency_ms"`
	PromptTokens     int     `json:"prompt_tokens"`
	CompletionTokens int     `json:"completion_tokens"`
}

// Features — результат первого этапа. Поля намеренно сведены к перечислениям
// и флагам: свободный текст здесь стал бы вторым местом, где может завестись
// ошибка, и его нельзя было бы проверить.
type Features struct {
	Visible  string `json:"visible"`  // yes, no, unknown — меняется ли внешне наблюдаемое поведение
	Artifact string `json:"artifact"` // yes, no — есть ли готовый код или диф на оценку
	Mode     string `json:"mode"`     // question, order — спрашивают или поручают
	Product  string `json:"product"`  // code, tests, text, answer — что является продуктом работы
	Raw      string `json:"raw"`
	Parsed   bool   `json:"parsed"`
}

// Result — итог одного прогона по одному входу.
type Result struct {
	Input string `json:"input"`
	Mode  string `json:"mode"` // monolith или multistage

	Features   *Features `json:"features,omitempty"`
	ContractQ  string    `json:"contract_answer,omitempty"` // yes или no
	Class      string    `json:"class"`
	FormatOK   bool      `json:"format_ok"`
	Consistent bool      `json:"consistent"`
	Conflict   string    `json:"conflict,omitempty"`

	Calls            []Call `json:"calls"`
	CallCount        int    `json:"call_count"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
	LatencyMS        int64  `json:"latency_ms"`
}

func (r *Result) add(c Call) {
	r.Calls = append(r.Calls, c)
	r.CallCount++
	r.PromptTokens += c.PromptTokens
	r.CompletionTokens += c.CompletionTokens
	r.LatencyMS += c.LatencyMS
}

// Monolith — один запрос, один ответ. Вариант A из задания.
func Monolith(c *llm.Client, sp *spec.Spec, input string) (Result, error) {
	r := Result{Input: input, Mode: "monolith", Consistent: true}
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
	r.add(Call{
		Stage: "monolith", Model: c.Model, Raw: ans.Raw,
		SeqProb: ans.SeqProb(), Margin: ans.Margin(),
		LatencyMS:    ans.LatencyMS,
		PromptTokens: ans.PromptTokens, CompletionTokens: ans.CompletionTokens,
	})
	if err != nil {
		return r, err
	}
	class, exact := sp.ParseClass(ans.Raw)
	r.Class = class
	r.FormatOK = exact
	return r, nil
}

// MultiStage — вариант B из задания.
//
// Параметр withNormalize управляет тем, выполняется ли первый этап. Он
// появился после первого прогона: выяснилось, что признаки нормализации,
// передаваемые во второй этап подсказкой, сбивают выбор класса, а на
// слабой модели первый этап ломается в трети случаев, не меняя при этом
// итоговой точности. Двухэтапный режим — проверка того, не лучше ли
// вовсе без него.
func MultiStage(cl Clients, sp *spec.Spec, input string, withNormalize bool) (Result, error) {
	mode := "multistage"
	if !withNormalize {
		mode = "twostage"
	}
	r := Result{Input: input, Mode: mode}

	var f *Features
	if withNormalize {
		var err error
		f, err = normalize(cl.Normalize, &r, input)
		if err != nil {
			return r, fmt.Errorf("этап 1: %w", err)
		}
		r.Features = f
	}

	class, contract, err := decide(cl.Decide, sp, &r, input, f)
	if err != nil {
		return r, fmt.Errorf("этап 2: %w", err)
	}
	r.ContractQ = contract
	r.Class = class
	r.FormatOK = sp.IsClass(class)

	assemble(&r, f, class)
	return r, nil
}

// normalize — первый этап. Формат ответа задан жёстко: четыре поля через
// точку с запятой, каждое из своего перечисления. Так его можно разобрать
// и проверить, а не гадать по прозе.
func normalize(c *llm.Client, r *Result, input string) (*Features, error) {
	system := strings.Join([]string{
		"Ты разбираешь формулировку задачи по репозиторию и выделяешь четыре признака.",
		"Отвечай ровно одной строкой в формате: visible=<yes|no>; artifact=<yes|no>; mode=<question|order>; product=<code|tests|text|answer>",
		"",
		"visible — изменится ли то, что видит внешний потребитель API: код ответа, форма JSON, набор полей, путь маршрута, сигнатура экспортированного метода.",
		"artifact — приложен ли к задаче готовый код, патч или диф, который просят оценить.",
		"mode — спрашивают об устройстве кода (question) или поручают что-то сделать (order).",
		"product — что будет результатом работы: правка кода (code), проверки (tests), документация (text) или объяснение без правок (answer).",
		"",
		"Никаких пояснений, только строка.",
	}, "\n")

	ans, err := c.Ask(llm.Request{
		Messages: []llm.Message{
			{Role: "system", Content: system},
			{Role: "user", Content: input},
		},
		Temperature: 0,
		MaxTokens:   48,
		Seed:        1,
	})
	r.add(Call{
		Stage: "normalize", Model: c.Model, Raw: ans.Raw,
		SeqProb: -1, Margin: -1, LatencyMS: ans.LatencyMS,
		PromptTokens: ans.PromptTokens, CompletionTokens: ans.CompletionTokens,
	})
	if err != nil {
		return nil, err
	}
	return parseFeatures(ans.Raw), nil
}

// parseFeatures разбирает строку признаков. Неизвестные значения не
// выбрасываются в ошибку, а превращаются в unknown: этап 2 должен уметь
// работать и на неполных признаках, иначе одна осечка первого этапа
// роняет весь конвейер.
func parseFeatures(raw string) *Features {
	f := &Features{Raw: raw, Visible: "unknown", Artifact: "no", Mode: "order", Product: "code"}
	got := 0
	for _, part := range strings.Split(strings.ToLower(raw), ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), "`\"'.,")
		switch key {
		case "visible":
			if value == "yes" || value == "no" {
				f.Visible = value
				got++
			}
		case "artifact":
			if value == "yes" || value == "no" {
				f.Artifact = value
				got++
			}
		case "mode":
			if value == "question" || value == "order" {
				f.Mode = value
				got++
			}
		case "product":
			switch value {
			case "code", "tests", "text", "answer":
				f.Product = value
				got++
			}
		}
	}
	f.Parsed = got == 4
	return f
}

// decide — второй этап, и здесь всё главное.
//
// Сначала задаётся отдельный бинарный вопрос про контракт. Если ответ
// утвердительный, класс определён, и второй вызов не нужен: contract-change
// по правилу приоритета из таксономии перекрывает любой другой класс.
// Экономия попутная, но приятная — на таких входах этапов оказывается два,
// а не три.
//
// Если ответ отрицательный, класс выбирается из оставшихся семи. Именно
// оставшихся: contract-change из списка убран, чтобы модель не могла
// вернуться к решению, которое уже принято на предыдущем шаге.
func decide(c *llm.Client, sp *spec.Spec, r *Result, input string, f *Features) (string, string, error) {
	// Промпт переписан после первого прогона. Прежняя версия перечисляла,
	// что относится к контракту, и модель начала отвечать yes везде, где
	// эти слова встречались: четыре бага уехали в contract-change только
	// потому, что в них упоминался код статуса.
	//
	// Ключевое различие — между «поведение меняется» и «поведение
	// приводится к тому, что уже описано». Первое ломает существующего
	// клиента, второе чинит расхождение с документацией. Теперь вопрос
	// задан через это различие, а перечисление признаков идёт после
	// него, а не вместо.
	contractSystem := strings.Join([]string{
		"Ты отвечаешь на один вопрос о задаче по сервису notes-api:",
		"СТАНЕТ ли внешнее поведение сервиса ДРУГИМ по сравнению с тем, как оно описано сейчас?",
		"",
		"Отвечай yes, только если после выполнения задачи существующий клиент,",
		"работающий по текущему описанию API, перестанет работать как раньше.",
		"",
		"Отвечай no, если задача приводит поведение К тому, что уже описано:",
		"код возвращает не то, что сказано в документации или в описании API,",
		"и его чинят. Это исправление расхождения, а не смена договорённости.",
		"",
		"Примеры ответа no:",
		"— «отвечает 200 вместо 204, хотя в описании API стоит 204» — описание не меняется, чинят код;",
		"— «возвращает null вместо пустого массива» — ожидаемое поведение уже задокументировано;",
		"— «добавить новый эндпоинт /healthz» — существующие клиенты не затронуты;",
		"— «убрать дублирование в хендлерах» — внешнее поведение прежнее.",
		"",
		"Примеры ответа yes:",
		"— «отдавать 201 вместо нынешнего 200» — меняется то, что клиент получает сегодня;",
		"— «переименовать поле text в body» — клиент перестанет находить поле;",
		"— «Service.List должен принимать фильтр» — меняется сигнатура, вызывающий код сломается;",
		"— «перенести POST /notes на /api/v1/notes, старый путь убрать» — старый путь исчезнет.",
		"",
		"Ответь ровно одним словом: yes или no.",
	}, "\n")

	user := input
	if f != nil && f.Visible != "unknown" {
		// Признак с первого этапа передаётся как подсказка, а не как
		// готовый ответ: этап 2 может с ним не согласиться.
		user = fmt.Sprintf("%s\n\n(предварительный разбор: изменение внешне наблюдаемого поведения — %s)", input, f.Visible)
	}

	ans, err := c.Ask(llm.Request{
		Messages: []llm.Message{
			{Role: "system", Content: contractSystem},
			{Role: "user", Content: user},
		},
		Temperature: 0,
		MaxTokens:   8,
		LogProbs:    true,
		TopLogProbs: 5,
		Seed:        1,
	})
	r.add(Call{
		Stage: "decide:contract", Model: c.Model, Raw: ans.Raw,
		SeqProb: ans.SeqProb(), Margin: ans.Margin(), LatencyMS: ans.LatencyMS,
		PromptTokens: ans.PromptTokens, CompletionTokens: ans.CompletionTokens,
	})
	if err != nil {
		return "", "", err
	}

	contract := "no"
	if strings.Contains(strings.ToLower(strings.TrimSpace(ans.Raw)), "yes") {
		contract = "yes"
	}
	if contract == "yes" {
		return "contract-change", contract, nil
	}

	rest := make([]string, 0, len(sp.Classes))
	for _, cls := range sp.Classes {
		if cls != "contract-change" {
			rest = append(rest, cls)
		}
	}
	sort.Strings(rest)

	classSystem := strings.Join([]string{
		"Ты классифицируешь задачу по репозиторию notes-api.",
		"Уже установлено, что публичный контракт эта задача не меняет.",
		"Выбери ровно один класс из списка: " + strings.Join(rest, ", ") + ".",
		"Ответ — одно слово, без пояснений.",
	}, "\n")

	hint := input
	if f != nil && f.Parsed {
		hint = fmt.Sprintf("%s\n\n(предварительный разбор: artifact=%s; mode=%s; product=%s)",
			input, f.Artifact, f.Mode, f.Product)
	}

	ans2, err := c.Ask(llm.Request{
		Messages: []llm.Message{
			{Role: "system", Content: classSystem},
			{Role: "user", Content: hint},
		},
		Temperature: 0,
		MaxTokens:   16,
		LogProbs:    true,
		TopLogProbs: 8,
		Seed:        1,
	})
	r.add(Call{
		Stage: "decide:class", Model: c.Model, Raw: ans2.Raw,
		SeqProb: ans2.SeqProb(), Margin: ans2.Margin(), LatencyMS: ans2.LatencyMS,
		PromptTokens: ans2.PromptTokens, CompletionTokens: ans2.CompletionTokens,
	})
	if err != nil {
		return "", contract, err
	}
	class, _ := sp.ParseClass(ans2.Raw)
	return class, contract, nil
}

// assemble — третий этап. Модель здесь не вызывается: сборка сводится к
// проверке того, что признаки первого этапа не противоречат выбранному
// классу, и это чистая логика.
//
// Задание допускает вызов модели и на этом этапе, но платить за него было бы
// странно: правила ниже — это ровно то, что записано в таксономии Дня 6,
// и спрашивать их у модели значит вносить шум там, где ответ известен точно.
func assemble(r *Result, f *Features, class string) {
	r.Consistent = true
	if f == nil || !f.Parsed {
		if f != nil && !f.Parsed {
			r.Consistent = false
			r.Conflict = "признаки первого этапа разобраны не полностью"
		}
		return
	}

	switch {
	case class == "research" && f.Mode == "order" && f.Product != "answer":
		r.Consistent = false
		r.Conflict = "класс research, но разбор говорит о поручении с правками"
	case class == "review" && f.Artifact == "no":
		r.Consistent = false
		r.Conflict = "класс review, но готового артефакта на оценку нет"
	case class == "testing" && f.Product == "text":
		r.Consistent = false
		r.Conflict = "класс testing, но продукт работы — текст"
	case class == "docs" && f.Product == "code":
		r.Consistent = false
		r.Conflict = "класс docs, но продукт работы — правка кода"
	case class == "refactor" && f.Visible == "yes":
		r.Consistent = false
		r.Conflict = "класс refactor, но внешнее поведение меняется"
	}
}
