// Package storage содержит реализации note.Repository. Пока это только
// потокобезопасное хранилище в памяти, но за интерфейсом может стоять и БД.
package storage

import (
	"context"
	"sort"
	"sync"

	"github.com/swanden/ai-challenge-advanced/week-1/task-5/notes-api/internal/note"
)

// MemoryRepo хранит заметки в памяти. Безопасен для конкурентного доступа:
// каждый метод защищён мьютексом, так как http-сервер обрабатывает запросы
// в нескольких горутинах одновременно.
type MemoryRepo struct {
	mu    sync.RWMutex
	notes map[string]note.Note
}

// NewMemoryRepo создаёт пустое хранилище, готовое к использованию.
func NewMemoryRepo() *MemoryRepo {
	return &MemoryRepo{notes: make(map[string]note.Note)}
}

// Create сохраняет новую заметку. Дубликат id считается ошибкой программиста
// (id генерирует сервис), поэтому здесь мы просто перезаписываем — но под
// блокировкой, чтобы не словить гонку.
func (r *MemoryRepo) Create(ctx context.Context, n note.Note) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes[n.ID] = n
	return nil
}

// Get возвращает копию заметки или note.ErrNotFound.
func (r *MemoryRepo) Get(ctx context.Context, id string) (note.Note, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	n, ok := r.notes[id]
	if !ok {
		return note.Note{}, note.ErrNotFound
	}
	return n, nil
}

// Update перезаписывает существующую заметку. Если её нет — note.ErrNotFound.
func (r *MemoryRepo) Update(ctx context.Context, n note.Note) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.notes[n.ID]; !ok {
		return note.ErrNotFound
	}
	r.notes[n.ID] = n
	return nil
}

// Delete удаляет заметку. Если её нет — note.ErrNotFound.
func (r *MemoryRepo) Delete(ctx context.Context, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.notes[id]; !ok {
		return note.ErrNotFound
	}
	delete(r.notes, id)
	return nil
}

// List возвращает все заметки, отсортированные по времени создания,
// чтобы выдача была детерминированной.
func (r *MemoryRepo) List(ctx context.Context) ([]note.Note, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []note.Note
	for _, n := range r.notes {
		out = append(out, n)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}
