package render

import (
	"bytes"
	"context"
	"io"
	"reflect"

	"github.com/a-h/templ"
)

// renderTempl renders a templ component into a byte slice.
func renderTempl(ctx context.Context, component templ.Component) ([]byte, error) {
	if isNilComponent(component) {
		return nil, nil
	}

	var b bytes.Buffer
	if err := component.Render(ctx, &b); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// isNilComponent guards against nil templ components hidden behind the
// interface — both an untyped nil and the classic typed-nil (a nil *T stored
// in a non-nil templ.Component interface), which a plain == nil check misses
// and which would otherwise panic when Render is called.
func isNilComponent(component templ.Component) bool {
	if component == nil {
		return true
	}
	value := reflect.ValueOf(component)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// rawHTML wraps pre-rendered HTML in a templ component.
func rawHTML(data []byte) templ.Component {
	return templ.ComponentFunc(func(_ context.Context, w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}

// join renders several components in order.
func join(components []templ.Component) templ.Component {
	return templ.ComponentFunc(func(ctx context.Context, w io.Writer) error {
		for _, component := range components {
			if err := component.Render(ctx, w); err != nil {
				return err
			}
		}
		return nil
	})
}

// wrapIsland wraps a server-rendered island in its marker element. The
// wrapper is the DOM anchor client-side web-vitals attribution walks
// (closest("[data-island]")) and the hook for the stylesheet's
// display:contents rule, which keeps the element out of layout while it
// stays in the DOM.
func wrapIsland(name string, html []byte) []byte {
	var b bytes.Buffer
	b.Grow(len(html) + len(name) + 40)
	b.WriteString(`<div class="ds-island" data-island="`)
	b.WriteString(templ.EscapeString(name))
	b.WriteString(`">`)
	b.Write(html)
	b.WriteString(`</div>`)
	return b.Bytes()
}
