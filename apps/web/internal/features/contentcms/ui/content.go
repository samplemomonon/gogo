// Package ui is the contentcms vertical's read-side page context: it owns the
// gateway's read-only editorial content contract. CMS data is untrusted input:
// renderers accept only the block types declared here and never accept
// executable HTML or browser-side code.
package ui

import (
	"context"
	"errors"
	"net/url"
	"strings"
)

var (
	ErrUnavailable = errors.New("editorial content source unavailable")
	ErrNotFound    = errors.New("editorial page not found")
)

// Reader is the only application-facing CMS capability. It can read published
// page content; it has no mutation or preview operation.
type Reader interface {
	ReadPublished(context.Context, string) (Page, error)
}

type disabledReader struct{}

func (disabledReader) ReadPublished(context.Context, string) (Page, error) {
	return Page{}, ErrNotFound
}

// DisabledReader keeps route composition stable when no Builder.io project is
// configured; public editorial URLs resolve as absent rather than as failures.
func DisabledReader() Reader { return disabledReader{} }

type Page struct {
	Slug   string  `json:"slug"`
	Title  string  `json:"title"`
	Blocks []Block `json:"blocks"`
}

type Block struct {
	Type     string    `json:"type"`
	Title    string    `json:"title,omitempty"`
	Body     string    `json:"body,omitempty"`
	ImageURL string    `json:"imageUrl,omitempty"`
	ImageAlt string    `json:"imageAlt,omitempty"`
	Label    string    `json:"label,omitempty"`
	Href     string    `json:"href,omitempty"`
	Items    []FAQItem `json:"items,omitempty"`
}

type FAQItem struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

// Validate checks content shape and links before it enters a renderer. The
// list is deliberately closed so new presentation capabilities require a
// reviewed code change and deployment.
func (p Page) Validate() error {
	if !validSlug(p.Slug) || strings.TrimSpace(p.Title) == "" || len(p.Title) > 240 {
		return errors.New("invalid editorial page identity")
	}
	if len(p.Blocks) > 50 {
		return errors.New("editorial page exceeds block limit")
	}
	for _, block := range p.Blocks {
		if len(block.Title) > 500 || len(block.Body) > 100_000 || len(block.Label) > 200 || len(block.ImageAlt) > 500 {
			return errors.New("editorial block exceeds field limit")
		}
		switch block.Type {
		case "hero", "banner":
			if block.ImageURL != "" && !safePublicURL(block.ImageURL) {
				return errors.New("editorial image URL must be HTTPS or a same-origin path")
			}
		case "markdown":
		case "faq":
			if len(block.Items) > 100 {
				return errors.New("FAQ block exceeds item limit")
			}
			for _, item := range block.Items {
				if strings.TrimSpace(item.Question) == "" || len(item.Question) > 500 || len(item.Answer) > 20_000 {
					return errors.New("invalid FAQ item")
				}
			}
		case "cta":
			if strings.TrimSpace(block.Label) == "" || !safePublicURL(block.Href) {
				return errors.New("CTA requires a label and safe destination")
			}
		default:
			return errors.New("unsupported editorial block type")
		}
	}
	return nil
}

func validSlug(slug string) bool {
	if slug == "" || len(slug) > 80 {
		return false
	}
	for _, r := range slug {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

func safePublicURL(raw string) bool {
	if strings.HasPrefix(raw, "/") && !strings.HasPrefix(raw, "//") && !strings.Contains(raw, "\\") {
		return true
	}
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil
}
