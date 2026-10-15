package render_test

import (
	"testing"

	"gogo/internal/shared/web/render"
)

func TestComposerFactory(t *testing.T) {
	t.Run("creates composer with TTL cache", func(t *testing.T) {
		factory := render.DefaultFactory()
		composer := factory.NewComposer()

		if composer == nil {
			t.Fatal("expected composer, got nil")
		}
	})

	t.Run("creates composer with custom cache factory", func(t *testing.T) {
		callCount := 0
		factory := render.NewComposerFactory(func() render.Cache {
			callCount++
			return render.NewTTLCache()
		})

		composer := factory.NewComposer()
		if composer == nil {
			t.Fatal("expected composer, got nil")
		}

		if callCount != 1 {
			t.Errorf("expected cache factory to be called once, got %d", callCount)
		}
	})

	t.Run("creates composer with nil cache when factory is nil", func(t *testing.T) {
		factory := render.NewComposerFactory(nil)
		composer := factory.NewComposer()

		if composer == nil {
			t.Fatal("expected composer, got nil")
		}
	})
}
