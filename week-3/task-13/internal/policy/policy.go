// Package policy решает, что делать с запросом и ответом.
//
// Ключевая часть — нормализация. Правила из detect ищут секрет в тексте как
// есть, а прятать его умеют: закодировать в base64, разорвать конкатенацией,
// раскидать по разным сообщениям одного запроса. Поэтому сканирование идёт по
// нескольким представлениям одного и того же запроса.
//
// И здесь же — размен, который надо мерить, а не декларировать. Вчерашний День
// 12 показал, что нормализация недоверенных данных повышает их читаемость и
// тем помогает атакующему. Тут она работает в обратную сторону: помогает
// детектору. Но цена та же — чем агрессивнее склейка, тем больше ложных
// срабатываний на нормальном тексте. Поэтому каждое представление помечается в
// находке полем Via: без него нельзя отличить сработавшее правило от
// сработавшей склейки.
package policy

import (
	"encoding/base64"
	"regexp"
	"strings"
	"unicode"

	"github.com/swanden/ai-challenge-advanced/week-3/task-13/internal/detect"
)

// Mode — режим входного слоя.
type Mode string

const (
	ModeOff   Mode = "off"   // не проверять вовсе
	ModeMask  Mode = "mask"  // маскировать всё, что можно, блокировать остальное
	ModeBlock Mode = "block" // блокировать при любой находке
)

// Verdict — решение по запросу.
type Verdict struct {
	Action   string           `json:"action"` // pass, masked, blocked
	Findings []detect.Finding `json:"findings,omitempty"`
	// Text — обезвреженный текст, который уйдёт в модель. Пуст при блокировке.
	Text string `json:"-"`
}

// zeroWidth убирается всегда: невидимые символы внутри ключа ломают правило и
// не несут смысла в обычном тексте.
var reZeroWidth = regexp.MustCompile("[\u200b\u200c\u200d\u2060\ufeff]")

// ScanInput проверяет одно текстовое поле запроса.
//
// Представлений четыре, и каждое ловит свой приём сокрытия. Порядок от
// точного к грубому: сначала текст как есть, потом снятие невидимых символов,
// потом раскодированный base64, и последней — склейка без разделителей,
// которая ловит разорванный секрет и она же даёт больше всего ложных
// срабатываний.
func ScanInput(text string, mode Mode) Verdict {
	if mode == ModeOff {
		return Verdict{Action: "pass", Text: text}
	}

	stripped := reZeroWidth.ReplaceAllString(text, "")

	// Основное представление: по нему же строится обезвреженный текст,
	// потому что позиции совпадают с оригиналом.
	findings, clean := detect.Scan(stripped)
	for i := range findings {
		findings[i].Via = "как есть"
	}

	// Дополнительные представления только сообщают о находке. Маскировать по
	// ним нельзя: участка, найденного после склейки, в исходном тексте не
	// существует, подменять нечего. Поэтому такая находка всегда блокирует —
	// к тому же выводу пришёл Буйко.
	extra := map[string]string{
		"base64": decodeBase64Blobs(stripped),
		"склейка без разделителей": glue(stripped),
	}
	for via, variant := range extra {
		if variant == "" || variant == stripped {
			continue
		}
		f2, _ := detect.Scan(variant)
		for _, f := range f2 {
			if hasKind(findings, f.Kind) {
				continue
			}
			f.Via = via
			f.Action = string(detect.Block)
			findings = append(findings, f)
		}
	}

	if len(findings) == 0 {
		return Verdict{Action: "pass", Text: text}
	}
	if mode == ModeBlock || detect.Worst(findings) == detect.Block {
		return Verdict{Action: "blocked", Findings: findings}
	}
	return Verdict{Action: "masked", Findings: findings, Text: clean}
}

// ScanOutput проверяет ответ модели.
//
// Помимо секретов ищутся три вещи, которых в нормальном ответе не бывает:
// дословный кусок системного промпта этого же запроса, опасные команды и
// подозрительные ссылки. Сравнение с системным промптом идёт по n-граммам —
// тот же приём, что в детекторе утечки Дня 11, и та же его слабость: пересказ
// своими словами не ловится.
func ScanOutput(answer, system string, mode Mode) Verdict {
	if mode == ModeOff {
		return Verdict{Action: "pass", Text: answer}
	}
	findings, clean := detect.Scan(answer)
	for i := range findings {
		findings[i].Via = "ответ модели"
		// Секрет, придуманный моделью, маскируется, а не блокирует: ответ
		// без него остаётся полезным, а сам он всё равно вымышленный.
		findings[i].Action = string(detect.Mask)
	}

	if system != "" && echoesSystem(answer, system) {
		findings = append(findings, detect.Finding{
			Kind: "system-prompt-echo", Action: string(detect.Block), Count: 1,
			Fingerprint: detect.Fingerprint(system), Via: "ответ модели",
		})
	}
	for _, m := range reDangerousCmd.FindAllString(answer, -1) {
		findings = append(findings, detect.Finding{
			Kind: "dangerous-command", Action: string(detect.Block), Count: 1,
			Fingerprint: detect.Fingerprint(m), Via: "ответ модели",
		})
		break
	}
	for _, m := range reSuspiciousURL.FindAllString(answer, -1) {
		findings = append(findings, detect.Finding{
			Kind: "suspicious-url", Action: string(detect.Mask), Count: 1,
			Fingerprint: detect.Fingerprint(m), Via: "ответ модели",
		})
		clean = strings.ReplaceAll(clean, m, "[REDACTED_URL]")
		break
	}

	if len(findings) == 0 {
		return Verdict{Action: "pass", Text: answer}
	}
	if detect.Worst(findings) == detect.Block {
		return Verdict{Action: "blocked", Findings: findings}
	}
	return Verdict{Action: "masked", Findings: findings, Text: clean}
}

var (
	reDangerousCmd  = regexp.MustCompile(`(?i)(?:curl|wget)\s+[^\n|]*\|\s*(?:ba)?sh|rm\s+-rf\s+/|:\(\)\{.*\};:`)
	reSuspiciousURL = regexp.MustCompile(`(?i)https?://(?:\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}|bit\.ly|tinyurl\.com|t\.co)[^\s)"']*`)
	reB64           = regexp.MustCompile(`[A-Za-z0-9+/]{24,}={0,2}`)
)

const ngram = 8

// echoesSystem ищет дословный фрагмент системного промпта в ответе.
func echoesSystem(answer, system string) bool {
	a := words(answer)
	s := words(system)
	if len(a) < ngram || len(s) < ngram {
		return false
	}
	need := map[string]bool{}
	for i := 0; i+ngram <= len(s); i++ {
		need[strings.Join(s[i:i+ngram], " ")] = true
	}
	for i := 0; i+ngram <= len(a); i++ {
		if need[strings.Join(a[i:i+ngram], " ")] {
			return true
		}
	}
	return false
}

func words(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// decodeBase64Blobs раскодирует все похожие на base64 куски и склеивает
// результат. Раскодированное никуда, кроме проверки, не передаётся.
func decodeBase64Blobs(s string) string {
	var b strings.Builder
	for _, blob := range reB64.FindAllString(s, -1) {
		for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding} {
			if raw, err := enc.DecodeString(blob); err == nil && printable(string(raw)) {
				b.WriteString(string(raw))
				b.WriteString(" ")
				break
			}
		}
	}
	return b.String()
}

// glue убирает всё, что может разделять куски секрета: кавычки, плюсы,
// пробелы, переносы. Так `"sk-" + "proj-abc"` снова становится ключом.
//
// Самое грубое из представлений и главный источник ложных срабатываний:
// склеенный текст перестаёт быть текстом, и случайные соседства выглядят как
// строки. Поэтому находка отсюда всегда помечается полем Via.
func glue(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '\'', '+', ' ', '\t', '\n', '\r', '\\', '`', ',':
			return -1
		}
		return r
	}, s)
}

func printable(s string) bool {
	if s == "" {
		return false
	}
	good := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsSpace(r) || unicode.IsPunct(r) {
			good++
		}
	}
	return float64(good)/float64(len([]rune(s))) > 0.9
}

func hasKind(list []detect.Finding, kind string) bool {
	for _, f := range list {
		if f.Kind == kind {
			return true
		}
	}
	return false
}
