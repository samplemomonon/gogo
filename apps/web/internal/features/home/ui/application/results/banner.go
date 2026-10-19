package results

// Banner is the home page's view of its editorial banner slot: the published
// hero/banner content the editorial CMS serves for the slot's slug, mapped into
// the fields this page renders. Like the rest of this package the shape is
// page-local — the CMS block struct and its wire format stay behind the remote
// port, and the page never sees them.
type Banner struct {
	Title    string
	ImageURL string
	ImageAlt string
}
