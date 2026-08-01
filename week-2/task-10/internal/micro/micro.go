// Package micro — первый уровень двухуровневого инференса: дешёвый
// классификатор, который отвечает сам либо признаётся, что не знает.
//
// Задание допускает три варианта первого уровня — маленькую LLM,
// классификацию на эмбеддингах или простой ML-классификатор, — и здесь
// реализованы все три. Основной, заданный по умолчанию, — n-граммы:
// он единственный не обращается ни к какой модели вообще, отвечает за
// микросекунды и полностью детерминирован.
//
// Ключевое отличие от Дня 8, где первый уровень тоже мог сказать «не
// уверен». Там уверенность означала «насколько модель убеждена в своём
// ответе», и День 7 измерил, что с правильностью она не связана: ошибки
// шли с вероятностью 1.00. Здесь уверенность значит другое — «насколько
// этот вход похож на то, что я уже видел». Это внешняя мера, а не
// самооценка, и она может оказаться работающей там, где самооценка
// провалилась. Проверка этого и есть содержание дня.
package micro

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/swanden/ai-challenge-advanced/week-2/task-10/internal/llm"
	"github.com/swanden/ai-challenge-advanced/week-2/task-10/internal/spec"
)

// Виды первого уровня.
const (
	KindNGram = "ngram" // символьные n-граммы, ближайшие соседи, без сети
	KindEmbed = "embed" // эмбеддинги через Ollama, ближайшие соседи
	KindTiny  = "tiny"  // маленькая LLM
)

// Статусы ответа первого уровня.
const (
	StatusOK     = "OK"
	StatusUnsure = "UNSURE"
)

// Answer — что вернул первый уровень.
type Answer struct {
	Class      string   `json:"class"`
	Status     string   `json:"status"`
	Confidence float64  `json:"confidence"`
	Reason     string   `json:"reason,omitempty"`
	Neighbours []string `json:"neighbours,omitempty"` // ближайшие примеры, для разбора
	LatencyUS  int64    `json:"latency_us"`
	Calls      int      `json:"calls"` // обращений к сети: у ngram всегда 0
}

// Params — настройки, зафиксированные до прогона. Обоснование в docs/micro.md.
type Params struct {
	Kind          string
	K             int     // сколько соседей учитывать
	MinSimilarity float64 // ближе которого сосед считается непохожим
	MinVotes      float64 // доля голосов за победивший класс
	NGramMin      int
	NGramMax      int
}

// Default возвращает настройки по умолчанию.
func Default() Params {
	return Params{
		Kind:          KindNGram,
		K:             5,
		MinSimilarity: 0.25,
		MinVotes:      0.60,
		NGramMin:      3,
		NGramMax:      4,
	}
}

// example — обучающий пример вместе с его векторным представлением.
type example struct {
	text   string
	class  string
	vector map[string]float64
	norm   float64
}

// Classifier — обученный первый уровень.
type Classifier struct {
	params   Params
	examples []example
	idf      map[string]float64
	tiny     *llm.Client
	sp       *spec.Spec
}

// NewNGram обучает классификатор на символьных n-граммах.
//
// Символьные, а не словесные, намеренно: формулировки короткие, в них
// много идентификаторов вроде Service.List и MemoryRepo, и словесная
// разбивка на таком материале даёт слишком разрежённые векторы. Символьные
// n-граммы вдобавок устойчивы к опечаткам — а в наборе проб есть вход,
// написанный с опечаткой в каждом слове.
func NewNGram(sp *spec.Spec, train []spec.Example, p Params) (*Classifier, error) {
	if len(train) == 0 {
		return nil, fmt.Errorf("обучающая выборка пуста")
	}
	c := &Classifier{params: p, sp: sp}

	// Первый проход: сколько примеров содержат каждую n-грамму.
	df := map[string]int{}
	raw := make([]map[string]float64, 0, len(train))
	for _, ex := range train {
		counts := ngrams(ex.User(), p.NGramMin, p.NGramMax)
		raw = append(raw, counts)
		for g := range counts {
			df[g]++
		}
	}

	// idf гасит вклад n-грамм, встречающихся почти везде: без него
	// векторы всех формулировок оказываются похожими друг на друга
	// просто потому, что написаны по-русски.
	c.idf = make(map[string]float64, len(df))
	n := float64(len(train))
	for g, d := range df {
		c.idf[g] = math.Log(1 + n/float64(d))
	}

	for i, ex := range train {
		v := weigh(raw[i], c.idf)
		c.examples = append(c.examples, example{
			text: ex.User(), class: ex.Class(),
			vector: v, norm: norm(v),
		})
	}
	return c, nil
}

// NewEmbed обучает классификатор на эмбеддингах: те же ближайшие соседи,
// но векторы приходят от модели.
func NewEmbed(sp *spec.Spec, train []spec.Example, p Params, emb *llm.Client) (*Classifier, error) {
	if len(train) == 0 {
		return nil, fmt.Errorf("обучающая выборка пуста")
	}
	c := &Classifier{params: p, sp: sp}
	for _, ex := range train {
		v, err := embed(emb, ex.User())
		if err != nil {
			return nil, fmt.Errorf("эмбеддинг обучающего примера: %w", err)
		}
		c.examples = append(c.examples, example{
			text: ex.User(), class: ex.Class(),
			vector: v, norm: norm(v),
		})
	}
	c.tiny = emb
	return c, nil
}

// NewTiny делает первым уровнем маленькую языковую модель.
func NewTiny(sp *spec.Spec, p Params, tiny *llm.Client) *Classifier {
	return &Classifier{params: p, sp: sp, tiny: tiny}
}

// Classify — ответ первого уровня.
func (c *Classifier) Classify(input string) (Answer, error) {
	switch c.params.Kind {
	case KindTiny:
		return c.classifyTiny(input)
	case KindEmbed:
		return c.classifyEmbed(input)
	default:
		return c.classifyNGram(input)
	}
}

func (c *Classifier) classifyNGram(input string) (Answer, error) {
	v := weigh(ngrams(input, c.params.NGramMin, c.params.NGramMax), c.idf)
	return c.vote(v), nil
}

func (c *Classifier) classifyEmbed(input string) (Answer, error) {
	v, err := embed(c.tiny, input)
	if err != nil {
		return Answer{Status: StatusUnsure, Reason: "эмбеддинг не получен"}, err
	}
	a := c.vote(v)
	a.Calls = 1
	return a, nil
}

// classifyTiny спрашивает маленькую модель. Уверенность берётся из
// logprobs — то есть это ровно та самооценка, про которую День 7 показал,
// что с правильностью она не связана. Вариант оставлен для сравнения.
func (c *Classifier) classifyTiny(input string) (Answer, error) {
	ans, err := c.tiny.Ask(llm.Request{
		Messages: []llm.Message{
			{Role: "system", Content: c.sp.SystemPrompt},
			{Role: "user", Content: input},
		},
		Temperature: 0, MaxTokens: 16, LogProbs: true, TopLogProbs: 8, Seed: 1,
	})
	// Задержка приводится к микросекундам, как у остальных вариантов:
	// у ngram она измеряется именно в них, и смешивать единицы в одном
	// поле нельзя.
	a := Answer{Calls: 1, LatencyUS: ans.LatencyMS * 1000}
	if err != nil {
		a.Status = StatusUnsure
		a.Reason = "вызов не удался"
		return a, err
	}
	class, exact := c.sp.ParseClass(ans.Raw)
	a.Class = class
	a.Confidence = ans.SeqProb()
	switch {
	case class == "":
		a.Status = StatusUnsure
		a.Reason = "ответ вне списка классов"
	case !exact:
		a.Status = StatusUnsure
		a.Reason = "ответ многословный"
	case a.Confidence >= 0 && a.Confidence < c.params.MinVotes:
		a.Status = StatusUnsure
		a.Reason = fmt.Sprintf("вероятность %.2f ниже порога %.2f", a.Confidence, c.params.MinVotes)
	default:
		a.Status = StatusOK
	}
	return a, nil
}

// vote — общая часть для n-грамм и эмбеддингов: находит K ближайших
// примеров и голосует.
//
// Уверенность складывается из двух величин, и обе должны пройти порог.
// Похожесть на ближайшего соседа отвечает на вопрос «видел ли я вообще
// что-то подобное»; доля голосов — на вопрос «согласны ли соседи между
// собой». Вход, похожий на обучающие примеры, но лежащий на границе
// двух классов, должен уходить наверх так же, как и вход, не похожий ни
// на что.
func (c *Classifier) vote(v map[string]float64) Answer {
	start := nowMicros()
	a := Answer{}
	vn := norm(v)
	if vn == 0 {
		a.Status = StatusUnsure
		a.Reason = "вход не дал ни одного известного признака"
		a.LatencyUS = nowMicros() - start
		return a
	}

	type scored struct {
		sim   float64
		class string
		text  string
	}
	all := make([]scored, 0, len(c.examples))
	for _, ex := range c.examples {
		all = append(all, scored{sim: cosine(v, vn, ex.vector, ex.norm), class: ex.class, text: ex.text})
	}
	sort.Slice(all, func(i, j int) bool { return all[i].sim > all[j].sim })

	k := c.params.K
	if k > len(all) {
		k = len(all)
	}
	top := all[:k]

	// Голоса взвешиваются похожестью: сосед, отстоящий далеко, влияет
	// меньше близкого.
	weights := map[string]float64{}
	total := 0.0
	for _, s := range top {
		weights[s.class] += s.sim
		total += s.sim
		a.Neighbours = append(a.Neighbours, fmt.Sprintf("%.2f %s: %.40s", s.sim, s.class, s.text))
	}

	best, bestW := "", 0.0
	classes := make([]string, 0, len(weights))
	for cls := range weights {
		classes = append(classes, cls)
	}
	sort.Strings(classes) // стабильность при равенстве
	for _, cls := range classes {
		if weights[cls] > bestW {
			best, bestW = cls, weights[cls]
		}
	}

	a.Class = best
	if total > 0 {
		a.Confidence = bestW / total
	}
	nearest := top[0].sim

	switch {
	case nearest < c.params.MinSimilarity:
		a.Status = StatusUnsure
		a.Reason = fmt.Sprintf("ближайший пример непохож: %.2f при пороге %.2f", nearest, c.params.MinSimilarity)
	case a.Confidence < c.params.MinVotes:
		a.Status = StatusUnsure
		a.Reason = fmt.Sprintf("соседи разошлись: %.2f голосов при пороге %.2f", a.Confidence, c.params.MinVotes)
	default:
		a.Status = StatusOK
	}
	a.LatencyUS = nowMicros() - start
	return a
}

// ngrams режет строку на символьные n-граммы длиной от lo до hi.
func ngrams(s string, lo, hi int) map[string]float64 {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	r := []rune(s)
	out := map[string]float64{}
	for n := lo; n <= hi; n++ {
		for i := 0; i+n <= len(r); i++ {
			out[string(r[i:i+n])]++
		}
	}
	return out
}

func weigh(counts, idf map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(counts))
	for g, c := range counts {
		w, ok := idf[g]
		if !ok {
			// n-грамма не встречалась в обучении: вклада не даёт,
			// но и вход из-за неё не отбрасывается.
			continue
		}
		out[g] = (1 + math.Log(c)) * w
	}
	return out
}

func norm(v map[string]float64) float64 {
	s := 0.0
	for _, x := range v {
		s += x * x
	}
	return math.Sqrt(s)
}

// cosine — косинусная близость: мера того, насколько два вектора смотрят
// в одну сторону, от 0 до 1.
func cosine(a map[string]float64, an float64, b map[string]float64, bn float64) float64 {
	if an == 0 || bn == 0 {
		return 0
	}
	// Обходим меньший словарь.
	short, long := a, b
	if len(b) < len(a) {
		short, long = b, a
	}
	dot := 0.0
	for k, x := range short {
		if y, ok := long[k]; ok {
			dot += x * y
		}
	}
	return dot / (an * bn)
}

func embed(c *llm.Client, text string) (map[string]float64, error) {
	vec, err := c.Embed(text)
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(vec))
	for i, x := range vec {
		out[fmt.Sprintf("%d", i)] = x
	}
	return out, nil
}

func nowMicros() int64 { return time.Now().UnixMicro() }
