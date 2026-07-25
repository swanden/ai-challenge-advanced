package note

import (
	"encoding/hex"
	"testing"
	"time"
)

// RandomID и SystemClock обязаны удовлетворять контрактам домена.
var (
	_ IDGenerator = RandomID{}
	_ Clock       = SystemClock{}
)

// TestRandomIDNewID фиксирует формат идентификатора: 16 hex-символов
// (8 случайных байт) и уникальность в пределах серии.
func TestRandomIDNewID(t *testing.T) {
	var gen RandomID

	tests := []struct {
		name  string
		count int
	}{
		{name: "single id", count: 1},
		{name: "many ids stay unique", count: 1000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen := make(map[string]struct{}, tt.count)
			for i := 0; i < tt.count; i++ {
				id := gen.NewID()

				if len(id) != 16 {
					t.Fatalf("len(NewID()) = %d, want 16 (id = %q)", len(id), id)
				}
				if _, err := hex.DecodeString(id); err != nil {
					t.Fatalf("NewID() = %q is not hex: %v", id, err)
				}
				if _, dup := seen[id]; dup {
					t.Fatalf("NewID() returned duplicate %q on iteration %d", id, i)
				}
				seen[id] = struct{}{}
			}
		})
	}
}

// TestSystemClockNow фиксирует инвариант «время всегда UTC» и то, что часы
// действительно системные (значение близко к time.Now и монотонно растёт).
func TestSystemClockNow(t *testing.T) {
	var clock SystemClock

	before := time.Now().UTC()
	got := clock.Now()
	after := time.Now().UTC()

	if got.Location() != time.UTC {
		t.Fatalf("Now().Location() = %v, want UTC", got.Location())
	}
	if _, offset := got.Zone(); offset != 0 {
		t.Fatalf("Now() zone offset = %d, want 0", offset)
	}
	if got.Before(before) || got.After(after) {
		t.Fatalf("Now() = %v, want between %v and %v", got, before, after)
	}
	if got.IsZero() {
		t.Fatal("Now() = zero time")
	}

	next := clock.Now()
	if next.Before(got) {
		t.Fatalf("clock went backwards: %v then %v", got, next)
	}
}
