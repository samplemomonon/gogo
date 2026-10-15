// Package results holds the profile page's read model: what the account page
// renders, projected from the auth contract so no auth wire type reaches a view.
package results

// Account is the signed-in shopper's account as the profile page renders it.
type Account struct {
	ID    string
	Name  string
	Email string
	Role  string
}
