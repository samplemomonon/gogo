package partials

import (
	"sort"
	"strings"

	"gogo/internal/features/sales/ui/application/results"
)

type categoryRow struct {
	Category *results.Category
	Depth    int
	Label    string
}

// categoryRows builds a stable parent-before-child view from the flat category
// contract. The service owns the hierarchy; this only shapes it for the seller UI.
func categoryRows(categories []*results.Category) []categoryRow {
	byID := make(map[string]*results.Category, len(categories))
	children := make(map[string][]*results.Category, len(categories))
	roots := make([]*results.Category, 0, len(categories))
	for _, category := range categories {
		if category == nil || category.ID == "" {
			continue
		}
		byID[category.ID] = category
	}

	for _, category := range byID {
		parent := byID[category.ParentID]
		if category.ParentID == "" || parent == nil || parent.ID == category.ID {
			roots = append(roots, category)
			continue
		}
		children[parent.ID] = append(children[parent.ID], category)
	}

	less := func(categories []*results.Category) {
		sort.Slice(categories, func(i, j int) bool {
			left, right := strings.ToLower(categories[i].Name), strings.ToLower(categories[j].Name)
			if left == right {
				return categories[i].ID < categories[j].ID
			}
			return left < right
		})
	}
	less(roots)
	for _, group := range children {
		less(group)
	}

	rows := make([]categoryRow, 0, len(byID))
	visited := make(map[string]bool, len(byID))
	var appendTree func(*results.Category, int)
	appendTree = func(category *results.Category, depth int) {
		if visited[category.ID] {
			return
		}
		visited[category.ID] = true
		label := category.Name
		if depth > 0 {
			label = strings.Repeat("　", depth-1) + "↳ " + label
		}
		rows = append(rows, categoryRow{Category: category, Depth: depth, Label: label})
		for _, child := range children[category.ID] {
			appendTree(child, depth+1)
		}
	}
	for _, root := range roots {
		appendTree(root, 0)
	}

	// A valid service response is an acyclic tree. Keep malformed/cyclic records
	// visible as roots rather than silently hiding categories from the seller.
	remaining := make([]*results.Category, 0, len(byID)-len(visited))
	for _, category := range byID {
		if !visited[category.ID] {
			remaining = append(remaining, category)
		}
	}
	less(remaining)
	for _, category := range remaining {
		appendTree(category, 0)
	}
	return rows
}

func categoryName(categories []*results.Category, id string) string {
	byID := make(map[string]*results.Category, len(categories))
	for _, category := range categories {
		if category != nil {
			byID[category.ID] = category
		}
	}

	path := make([]string, 0, 4)
	seen := make(map[string]bool, len(byID))
	for id != "" && !seen[id] {
		seen[id] = true
		category := byID[id]
		if category == nil {
			break
		}
		path = append(path, category.Name)
		id = category.ParentID
	}
	if len(path) == 0 {
		return "—"
	}
	for left, right := 0, len(path)-1; left < right; left, right = left+1, right-1 {
		path[left], path[right] = path[right], path[left]
	}
	return strings.Join(path, " / ")
}
