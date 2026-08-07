// Package detect ищет секреты и персональные данные в тексте.
//
// Правила разделены по тяжести последствий, а не по типу строки. Ключ доступа,
// уехавший к провайдеру модели, — это компрометация, его нельзя ни замаскировать
// «на всякий случай», ни пропустить: запрос блокируется. Персональные данные —
// адрес, телефон, карта — вредны в чужих логах, но запрос без них остаётся
// осмысленным, поэтому они маскируются, а работа продолжается.
//
// Разделение взято не из головы: и dpmn, и Буйко пришли к нему независимо, и у
// обоих маскирование стоит по умолчанию именно потому, что сохраняет
// работоспособность приложения.
package detect

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// Action — что делать с находкой.
type Action string

const (
	Block Action = "block" // запрос в модель не уходит
	Mask  Action = "mask"  // значение подменяется и запрос идёт дальше
)

// Rule — одно правило поиска.
type Rule struct {
	Kind        string
	Action      Action
	Placeholder string
	re          *regexp.Regexp
	// verify — дополнительная проверка совпадения. Нужна там, где формы
	// одной регуляркой не отличить: шестнадцать цифр могут быть картой, а
	// могут быть номером заказа.
	verify func(string) bool
}

// Finding — одна находка.
type Finding struct {
	Kind        string `json:"kind"`
	Action      string `json:"action"`
	Count       int    `json:"count"`
	Fingerprint string `json:"fingerprint"`
	Where       string `json:"where,omitempty"`
	// Via — как найдено: в тексте как есть, после раскодирования base64 или
	// после склейки кусков. Нужно отчёту: без этого не отличить «правило
	// сработало» от «сработала нормализация перед правилом».
	Via string `json:"via,omitempty"`
}

// Rules — набор правил.
//
// Порядок важен: сначала то, что блокирует, потом то, что маскирует. Если в
// запросе есть и ключ, и адрес, решение принимается по худшей находке.
var Rules = []Rule{
	{
		Kind: "openai-key", Action: Block, Placeholder: "[REDACTED_API_KEY]",
		re: regexp.MustCompile(`sk-(?:proj-)?[A-Za-z0-9_\-]{16,}`),
	},
	{
		Kind: "github-token", Action: Block, Placeholder: "[REDACTED_API_KEY]",
		re: regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{16,}`),
	},
	{
		Kind: "aws-access-key-id", Action: Block, Placeholder: "[REDACTED_API_KEY]",
		re: regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	},
	{
		Kind: "anthropic-key", Action: Block, Placeholder: "[REDACTED_API_KEY]",
		re: regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{16,}`),
	},
	{
		Kind: "slack-token", Action: Block, Placeholder: "[REDACTED_API_KEY]",
		re: regexp.MustCompile(`xox[baprs]-[A-Za-z0-9\-]{10,}`),
	},
	{
		Kind: "private-key", Action: Block, Placeholder: "[REDACTED_PRIVATE_KEY]",
		re: regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
	},
	{
		Kind: "db-url", Action: Block, Placeholder: "[REDACTED_DB_URL]",
		re: regexp.MustCompile(`(?i)\b(?:postgres|postgresql|mysql|mongodb(?:\+srv)?|redis)://[^\s:@]+:[^\s@]+@[^\s]+`),
	},
	{
		Kind: "card", Action: Mask, Placeholder: "[REDACTED_CARD]",
		re:     regexp.MustCompile(`\b(?:\d[ \-]?){12,18}\d\b`),
		verify: luhn,
	},
	{
		Kind: "email", Action: Mask, Placeholder: "[REDACTED_EMAIL]",
		re: regexp.MustCompile(`\b[\w.+-]+@[\w-]+\.[\w.-]{2,}\b`),
	},
	{
		Kind: "phone", Action: Mask, Placeholder: "[REDACTED_PHONE]",
		re: regexp.MustCompile(`(?:\+7|\+1|8)[ \-(]?\d{3}[ \-)]?\d{3}[ \-]?\d{2}[ \-]?\d{2}\b`),
	},
}

// Scan ищет находки в тексте и возвращает их вместе с обезвреженным текстом.
//
// Маскирование выполняется всегда, даже когда решение — блокировать: текст с
// подменёнными значениями нужен аудиту. Писать в лог исходный запрос нельзя, и
// это не гипотетический риск: у dpmn первая версия гейтвея писала в аудит-лог
// заблокированный ключ открытым текстом, то есть устраивала ровно ту утечку,
// ради предотвращения которой стоит.
func Scan(text string) ([]Finding, string) {
	var out []Finding
	clean := text
	for _, r := range Rules {
		matches := r.re.FindAllString(clean, -1)
		var kept []string
		for _, m := range matches {
			if r.verify != nil && !r.verify(m) {
				continue
			}
			kept = append(kept, m)
		}
		if len(kept) == 0 {
			continue
		}
		out = append(out, Finding{
			Kind:        r.Kind,
			Action:      string(r.Action),
			Count:       len(kept),
			Fingerprint: Fingerprint(kept[0]),
		})
		for _, m := range kept {
			clean = strings.ReplaceAll(clean, m, r.Placeholder)
		}
	}
	return out, clean
}

// Worst возвращает самое строгое решение по набору находок.
func Worst(findings []Finding) Action {
	for _, f := range findings {
		if f.Action == string(Block) {
			return Block
		}
	}
	return Mask
}

// Fingerprint — отпечаток значения для аудита.
//
// В лог попадает он, а не значение: по отпечатку можно понять, что это тот же
// самый ключ, что вчера, и нельзя восстановить сам ключ.
func Fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// luhn проверяет номер карты контрольной суммой.
//
// Без этой проверки правило ловило бы любые шестнадцать цифр подряд — номера
// заказов, идентификаторы сборок, телефонные серии. Ложное срабатывание тут
// стоит дорого: маскирование портит запрос, а пользователь не понимает, почему
// ассистент отвечает мимо.
func luhn(s string) bool {
	var digits []int
	for _, r := range s {
		if r >= '0' && r <= '9' {
			digits = append(digits, int(r-'0'))
		}
	}
	if len(digits) < 13 || len(digits) > 19 {
		return false
	}
	sum := 0
	double := false
	for i := len(digits) - 1; i >= 0; i-- {
		d := digits[i]
		if double {
			d *= 2
			if d > 9 {
				d -= 9
			}
		}
		sum += d
		double = !double
	}
	return sum%10 == 0
}

// Describe печатает находки одной строкой — для сообщения об отказе.
func Describe(findings []Finding) string {
	var parts []string
	for _, f := range findings {
		parts = append(parts, fmt.Sprintf("%s ×%d (отпечаток %s)", f.Kind, f.Count, f.Fingerprint))
	}
	return strings.Join(parts, ", ")
}
