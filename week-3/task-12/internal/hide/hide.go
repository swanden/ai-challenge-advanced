// Package hide прячет полезную нагрузку внутри носителя.
//
// Каждый способ — отдельная ось замера, а не украшение. Вчерашний День 11 и
// работы других участников сходятся в том, что слой очистки силён ровно
// настолько, насколько атакующий сам пометил свою инструкцию: комментарий,
// нулевой шрифт, невидимый символ — всё это признаки, которые ищет регулярка.
// Поэтому здесь же лежит пятый «способ» — Visible, при котором не прячется
// ничего. Он контрольный: очистке нечего резать, и работать остаются только
// слои, обращённые к модели.
package hide

import (
	"fmt"
	"strings"
)

// Method — способ размещения нагрузки.
type Method string

const (
	Comment  Method = "html-comment"  // <!-- ... -->
	Style    Method = "hidden-style"  // белым по белому, нулевой шрифт
	ZeroWide Method = "zero-width"    // невидимые символы внутри слов
	LinkText Method = "md-link-title" // нагрузка в title markdown-ссылки
	Visible  Method = "visible"       // не прячется вовсе — контроль
)

// Methods перечисляет способы; Visible идёт последним, чтобы в отчётах
// контроль стоял отдельно от настоящих приёмов.
var Methods = []Method{Comment, Style, ZeroWide, LinkText, Visible}

// Parse разбирает имя способа.
func Parse(s string) (Method, bool) {
	for _, m := range Methods {
		if string(m) == s {
			return m, true
		}
	}
	return "", false
}

// Hidden сообщает, оставляет ли способ следы, которые может найти очистка.
func (m Method) Hidden() bool { return m != Visible }

// Apply оборачивает нагрузку выбранным способом.
func Apply(m Method, payload string) string {
	switch m {
	case Comment:
		return "<!-- " + payload + " -->"
	case Style:
		return `<div style="color:#ffffff;background:#ffffff;font-size:1px">` + payload + `</div>`
	case ZeroWide:
		return spread(payload)
	case LinkText:
		// Нагрузка едет в атрибуте title: человек видит слово «подробнее»,
		// модель читает всю строку целиком.
		return fmt.Sprintf(`[подробнее](https://docs.example.com/notes-api "%s")`,
			strings.ReplaceAll(payload, `"`, `'`))
	case Visible:
		return payload
	}
	return payload
}

// zeroWidthSpace — символ, которым разрывают слова.
const zeroWidthSpace = "\u200b"

// spread вставляет невидимый символ внутрь каждого слова длиннее трёх букв.
//
// Смысл не в невидимости самого текста — он остаётся читаемым. Смысл в том,
// что наивный фильтр по подстроке перестаёт находить ключевые слова:
// «игнорируй» с разрывом внутри для поиска уже не «игнорируй». Очистка обязана
// снимать эти символы первым шагом, до всех остальных правил.
func spread(payload string) string {
	var b strings.Builder
	for _, word := range strings.Split(payload, " ") {
		r := []rune(word)
		if len(r) > 3 {
			b.WriteString(string(r[:2]) + zeroWidthSpace + string(r[2:]))
		} else {
			b.WriteString(word)
		}
		b.WriteString(" ")
	}
	return strings.TrimSpace(b.String())
}
