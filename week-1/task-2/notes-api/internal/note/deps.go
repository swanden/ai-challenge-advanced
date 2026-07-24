package note

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

// RandomID реализует IDGenerator через crypto/rand. Пустой struct — состояния нет.
type RandomID struct{}

// NewID возвращает 16-символьный hex-идентификатор. При сбое системного
// генератора случайностей паниковать нельзя молча — но в учебном сервисе
// мы считаем такой сбой фатальным на уровне ОС и пробрасываем панику.
func (RandomID) NewID() string {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		panic("note: system entropy source failed: " + err.Error())
	}
	return hex.EncodeToString(buf)
}

// SystemClock реализует Clock через реальные системные часы.
type SystemClock struct{}

// Now возвращает текущее время в UTC, чтобы метки не зависели от зоны сервера.
func (SystemClock) Now() time.Time { return time.Now().UTC() }
