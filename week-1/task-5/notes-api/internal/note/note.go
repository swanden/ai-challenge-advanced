// Package note содержит доменную модель заметки и бизнес-логику работы с ней.
// Слой ничего не знает про HTTP и про конкретное хранилище — он оперирует
// только интерфейсом Repository, поэтому его легко тестировать и переиспользовать.
package note

import (
	"context"
	"errors"
	"strings"
	"time"
)

// ErrNotFound возвращается, когда заметка с запрошенным id отсутствует.
// Транспортный слой сам решает, как отобразить эту ошибку (например, как 404).
var ErrNotFound = errors.New("note: not found")

// ErrEmptyText возвращается при попытке создать или обновить заметку без текста.
var ErrEmptyText = errors.New("note: text is empty")

// Note — доменная модель заметки. Поля экспортируются для сериализации,
// но заполняются только через конструкторы и методы сервиса.
type Note struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Repository — контракт хранилища заметок. Слой note зависит от абстракции,
// а не от конкретной реализации (in-memory, БД и т.п.).
type Repository interface {
	Create(ctx context.Context, n Note) error
	Get(ctx context.Context, id string) (Note, error)
	Update(ctx context.Context, n Note) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]Note, error)
}

// IDGenerator возвращает уникальный идентификатор для новой заметки.
// Вынесен в интерфейс, чтобы в тестах подставлять детерминированный генератор.
type IDGenerator interface {
	NewID() string
}

// Clock отдаёт текущее время. Абстракция нужна, чтобы фиксировать время в тестах.
type Clock interface {
	Now() time.Time
}

// Service инкапсулирует бизнес-правила работы с заметками.
type Service struct {
	repo  Repository
	ids   IDGenerator
	clock Clock
}

// NewService собирает сервис из его зависимостей. Все зависимости обязательны:
// передача nil — ошибка программиста, а не сценарий рантайма.
func NewService(repo Repository, ids IDGenerator, clock Clock) *Service {
	return &Service{repo: repo, ids: ids, clock: clock}
}

// Create проверяет входные данные, проставляет id и временные метки и сохраняет заметку.
func (s *Service) Create(ctx context.Context, text string) (Note, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Note{}, ErrEmptyText
	}

	now := s.clock.Now()
	n := Note{
		ID:        s.ids.NewID(),
		Text:      text,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.repo.Create(ctx, n); err != nil {
		return Note{}, err
	}
	return n, nil
}

// Get возвращает заметку по id или ErrNotFound, если её нет.
func (s *Service) Get(ctx context.Context, id string) (Note, error) {
	return s.repo.Get(ctx, id)
}

// UpdateText частично обновляет заметку: заменяет текст и проставляет UpdatedAt.
// Валидация текста — та же, что при создании: пустой текст недопустим.
func (s *Service) UpdateText(ctx context.Context, id, text string) (Note, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Note{}, ErrEmptyText
	}

	n, err := s.repo.Get(ctx, id)
	if err != nil {
		return Note{}, err
	}

	n.Text = text
	n.UpdatedAt = s.clock.Now()
	if err := s.repo.Update(ctx, n); err != nil {
		return Note{}, err
	}
	return n, nil
}

// Delete удаляет заметку по id или возвращает ErrNotFound, если её нет.
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

// List возвращает заметки, порядок определяется хранилищем. Если query непустой,
// возвращаются только заметки, чей текст содержит query без учёта регистра.
func (s *Service) List(ctx context.Context, query string) ([]Note, error) {
	notes, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	if query == "" {
		return notes, nil
	}

	query = strings.ToLower(query)
	out := make([]Note, 0, len(notes))
	for _, n := range notes {
		if strings.Contains(strings.ToLower(n.Text), query) {
			out = append(out, n)
		}
	}
	return out, nil
}

// Count возвращает количество хранимых заметок.
func (s *Service) Count(ctx context.Context) (int, error) {
	notes, err := s.repo.List(ctx)
	if err != nil {
		return 0, err
	}
	return len(notes), nil
}
