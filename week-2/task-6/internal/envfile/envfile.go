// Package envfile читает файл с переменными окружения — тот самый .env,
// который лежит в корне проекта, — чтобы ключи не приходилось экспортировать
// руками перед каждым запуском.
//
// Внешних зависимостей нет: формат простой, и тянуть ради него библиотеку
// в проект, где во всей первой неделе только стандартная библиотека, незачем.
//
// Уже заданные переменные окружения имеют приоритет над файлом. Это важно:
// разовый запуск с другим ключом делается через
// OPENAI_API_KEY=... go run ..., и файл этого не должен ломать.
package envfile

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// candidates — имена, под которыми обычно лежит файл окружения.
var candidates = []string{".env", "env", ".env.local"}

// maxDepth — насколько высоко подниматься по дереву каталогов в поисках файла.
const maxDepth = 5

// Load загружает переменные из файла окружения.
//
// Если explicit не пуст, читается именно он, и его отсутствие — ошибка.
// Иначе файл ищется в текущем каталоге и выше по дереву; если ничего не
// нашлось, это не ошибка — переменные могли быть заданы иначе.
//
// Возвращает путь фактически прочитанного файла (пустая строка, если файла нет).
func Load(explicit string) (string, error) {
	if explicit != "" {
		if err := parse(explicit); err != nil {
			return "", err
		}
		return explicit, nil
	}

	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for i := 0; i < maxDepth; i++ {
		for _, name := range candidates {
			path := filepath.Join(dir, name)
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			if err := parse(path); err != nil {
				return "", err
			}
			return path, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", nil
}

// parse читает файл и выставляет переменные, не затирая уже заданные.
func parse(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("файл окружения %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		raw = strings.TrimPrefix(raw, "export ")

		key, value, found := strings.Cut(raw, "=")
		if !found {
			// Строка без знака равенства — не переменная. Молча
			// пропускаем: в .env часто лежат заметки без решётки.
			continue
		}
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		value = strings.TrimSpace(value)
		value = unquote(value)

		if _, already := os.LookupEnv(key); already {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("%s:%d: %w", path, line, err)
		}
	}
	return sc.Err()
}

// unquote снимает окружающие кавычки, если они парные.
func unquote(s string) string {
	if len(s) < 2 {
		return s
	}
	first, last := s[0], s[len(s)-1]
	if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}
