// Package views is the billing module's presentation model: it renders the
// module's read model (activity entries) into Templ components. It mirrors
// the catalog/seller page views but owns no catalog code — the projection
// it renders is the billing module's own.
package views

import (
	"fmt"

	"gogo/internal/features/billing/activity"
)

// badgeTone maps an action to a bare daisyUI tone (ui.Badge adds the prefix).
func badgeTone(action activity.Action) string {
	switch action {
	case activity.ActionCreated:
		return "success"
	case activity.ActionUpdated:
		return "warning"
	case activity.ActionDeleted:
		return "error"
	default:
		return "neutral"
	}
}

// statusText summarizes the observed event count for the landing hero.
func statusText(count int) string {
	return fmt.Sprintf("%d event produk teramati lewat bus event modul", count)
}
