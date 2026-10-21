package domain

// Preset is a reusable, server-defined document fragment offered in the
// editor. Presets use the same registry and validation as every other
// component; inserting one is identical to building it by hand.
type Preset struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Category    string    `json:"category"`
	Component   Component `json:"component"`
}

// BuiltinPresets returns fresh preset trees. IDs are placeholders: the editor
// remaps every node when inserting so two insertions never collide.
func BuiltinPresets() []Preset {
	return []Preset{
		{
			ID: "hero-split", Name: "Split hero", Category: CategoryLayout,
			Description: "Heading, supporting copy, and a call to action in a two-column layout.",
			Component: Component{
				ID: "preset-hero", Type: "container",
				Props: map[string]any{"layout": "grid", "columns": float64(2), "gap": "lg", "padding": "xl", "maxWidth": "wide", "rounded": true},
				Children: []Component{
					{
						ID: "preset-hero-copy", Type: "container",
						Props: map[string]any{"layout": "stack", "gap": "sm", "padding": "none"},
						Children: []Component{
							{ID: "preset-hero-heading", Type: "heading", Props: map[string]any{"text": "A clear, useful headline", "level": "h1", "size": "2xl", "weight": "bold"}},
							{ID: "preset-hero-text", Type: "text", Props: map[string]any{"text": "Explain what the reader gets in one or two sentences.", "size": "lg", "color": ""}},
							{ID: "preset-hero-button", Type: "button", Props: map[string]any{"label": "Get started", "href": "/", "variant": "primary", "size": "lg"}},
						},
					},
					{
						ID: "preset-hero-art", Type: "container",
						Props: map[string]any{"layout": "stack", "gap": "md", "padding": "md", "background": "#eef2ff", "rounded": true},
						Children: []Component{
							{ID: "preset-hero-image", Type: "image", Props: map[string]any{"src": "/assets/img/placeholder.svg", "alt": "Illustration for the hero section", "aspect": "video", "width": "full", "fit": "cover", "rounded": true}},
						},
					},
				},
			},
		},
		{
			ID: "image-text", Name: "Image and text", Category: CategoryLayout,
			Description: "A responsive image beside supporting copy and one action, built from existing components.",
			Component: Component{
				ID: "preset-image-text", Type: "container",
				Props: map[string]any{"layout": "grid", "columns": float64(2), "gap": "lg", "padding": "xl", "maxWidth": "wide", "rounded": true},
				Children: []Component{
					{ID: "preset-image-text-image", Type: "image", Props: map[string]any{"src": "/assets/img/placeholder.svg", "alt": "Illustration for this section", "aspect": "video", "width": "full", "fit": "cover", "rounded": true}},
					{
						ID: "preset-image-text-copy", Type: "container",
						Props: map[string]any{"layout": "stack", "gap": "sm", "padding": "none"},
						Children: []Component{
							{ID: "preset-image-text-heading", Type: "heading", Props: map[string]any{"text": "A section that explains the value", "level": "h2", "size": "xl", "weight": "bold"}},
							{ID: "preset-image-text-body", Type: "text", Props: map[string]any{"text": "Use escaped text blocks for the supporting copy. Keep the image on the left and the action on the right, or reverse the children in the builder.", "size": "md"}},
							{ID: "preset-image-text-button", Type: "button", Props: map[string]any{"label": "Learn more", "href": "/", "variant": "outline", "size": "md"}},
						},
					},
				},
			},
		},
		{
			ID: "feature-grid", Name: "Three feature cards", Category: CategoryLayout,
			Description: "A responsive three-column grid of short value propositions.",
			Component: Component{
				ID: "preset-features", Type: "container",
				Props: map[string]any{"layout": "grid", "columns": float64(3), "gap": "md", "padding": "lg", "maxWidth": "wide", "rounded": true},
				Children: []Component{
					featureCard("preset-feature-a", "Fast", "Every page is rendered on the server and validated on save."),
					featureCard("preset-feature-b", "Accessible", "Keyboard selection, ordered headings, and real labels come built in."),
					featureCard("preset-feature-c", "Safe", "Page JSON never executes scripts or arbitrary HTML."),
				},
			},
		},
		{
			ID: "gallery", Name: "Image gallery", Category: CategoryLayout,
			Description: "A responsive image grid composed from a container and image components.",
			Component: Component{
				ID: "preset-gallery", Type: "container",
				Props: map[string]any{"layout": "grid", "columns": float64(3), "gap": "md", "padding": "lg", "maxWidth": "wide", "rounded": true},
				Children: []Component{
					galleryImage("preset-gallery-a", "Gallery image one"),
					galleryImage("preset-gallery-b", "Gallery image two"),
					galleryImage("preset-gallery-c", "Gallery image three"),
				},
			},
		},
		{
			ID: "rich-text", Name: "Rich text section", Category: CategoryContent,
			Description: "Long-form copy composed from semantic headings and escaped text blocks; HTML and Markdown are not accepted here.",
			Component: Component{
				ID: "preset-rich-text", Type: "container",
				Props: map[string]any{"layout": "stack", "gap": "sm", "padding": "lg", "maxWidth": "narrow", "rounded": true},
				Children: []Component{
					{ID: "preset-rich-text-heading", Type: "heading", Props: map[string]any{"text": "A clear section heading", "level": "h2", "size": "xl", "weight": "bold"}},
					{ID: "preset-rich-text-intro", Type: "text", Props: map[string]any{"text": "Write the opening paragraph as escaped text. Each paragraph is its own text block, which keeps the content safe and the reading rhythm explicit.", "size": "md"}},
					{ID: "preset-rich-text-body", Type: "text", Props: map[string]any{"text": "Add another text block for the next paragraph. The native registry intentionally does not execute HTML, scripts, or arbitrary rich-text markup.", "size": "md"}},
				},
			},
		},
		{
			ID: "faq", Name: "FAQ stack", Category: CategoryContent,
			Description: "A question-and-answer stack using headings and escaped text; answers are not collapsed by a script.",
			Component: Component{
				ID: "preset-faq", Type: "container",
				Props: map[string]any{"layout": "stack", "gap": "md", "padding": "lg", "maxWidth": "narrow", "rounded": true},
				Children: []Component{
					{ID: "preset-faq-heading", Type: "heading", Props: map[string]any{"text": "Frequently asked questions", "level": "h2", "size": "xl", "weight": "bold"}},
					faqItem("preset-faq-a", "How do drafts work?", "Saving a page changes only the draft. Publishing validates the full document and updates the public snapshot."),
					faqItem("preset-faq-b", "Can a draft appear publicly?", "No. Public requests read the published snapshot only; draft preview requires an authenticated editor."),
					faqItem("preset-faq-c", "Is arbitrary HTML allowed?", "No. Text is escaped and unknown component types are rejected before rendering."),
				},
			},
		},
		{
			ID: "cta-band", Name: "Call to action band", Category: CategoryContent,
			Description: "A single, focused action with supporting text.",
			Component: Component{
				ID: "preset-cta", Type: "container",
				Props: map[string]any{"layout": "stack", "gap": "sm", "padding": "lg", "background": "#312e81", "rounded": true},
				Children: []Component{
					{ID: "preset-cta-heading", Type: "heading", Props: map[string]any{"text": "Ready when you are", "level": "h2", "size": "xl", "weight": "bold", "color": "#ffffff", "align": "center"}},
					{ID: "preset-cta-text", Type: "text", Props: map[string]any{"text": "Start with the builder and publish when the page is ready.", "size": "md", "color": "#e0e7ff", "align": "center"}},
					{ID: "preset-cta-button", Type: "button", Props: map[string]any{"label": "Open the builder", "href": "/admin/content/pages", "variant": "secondary", "size": "md", "align": "center"}},
				},
			},
		},
		{
			ID: "contact-form", Name: "Contact form", Category: CategoryForms,
			Description: "A working form submitted to this page's protected endpoint.",
			Component: Component{
				ID: "preset-form", Type: "form",
				Props: map[string]any{
					"title": "Contact us", "submitLabel": "Send message", "successMessage": "Thank you — your message was received.",
					"fields": []any{
						map[string]any{"name": "name", "label": "Your name", "type": "text", "required": true},
						map[string]any{"name": "email", "label": "Email address", "type": "email", "required": true},
						map[string]any{"name": "message", "label": "Message", "type": "textarea", "required": true},
					},
				},
			},
		},
		{
			ID: "ad-break", Name: "Advertisement break", Category: CategoryAdvertising,
			Description: "A labelled reserved placement between content blocks.",
			Component: Component{
				ID: "preset-ad", Type: "ad-slot",
				Props: map[string]any{"slot": "content-leaderboard", "label": "Advertisement", "format": "leaderboard", "active": true},
			},
		},
	}
}

func featureCard(id, title, body string) Component {
	return Component{
		ID: id, Type: "container",
		Props: map[string]any{"layout": "stack", "gap": "xs", "padding": "md", "rounded": true},
		Children: []Component{
			{ID: id + "-heading", Type: "heading", Props: map[string]any{"text": title, "level": "h3", "size": "lg", "weight": "semibold"}},
			{ID: id + "-text", Type: "text", Props: map[string]any{"text": body, "size": "sm", "color": "#4b5563"}},
		},
	}
}

func galleryImage(id, alt string) Component {
	return Component{
		ID: id, Type: "image",
		Props: map[string]any{"src": "/assets/img/placeholder.svg", "alt": alt, "aspect": "square", "width": "full", "fit": "cover", "rounded": true},
	}
}

func faqItem(id, question, answer string) Component {
	return Component{
		ID: id, Type: "container",
		Props: map[string]any{"layout": "stack", "gap": "xs", "padding": "none"},
		Children: []Component{
			{ID: id + "-question", Type: "heading", Props: map[string]any{"text": question, "level": "h3", "size": "lg", "weight": "semibold"}},
			{ID: id + "-answer", Type: "text", Props: map[string]any{"text": answer, "size": "md"}},
		},
	}
}
