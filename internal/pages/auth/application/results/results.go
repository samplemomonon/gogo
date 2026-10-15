// Package results holds the auth page's own read model: what the page knows
// about a sign-in, in the page's language rather than the auth contract's.
package results

import "time"

// Session is the page's view of a signed-in session. It is deliberately not the
// contract's wire Session: the page needs the identity and the window, and
// nothing else, so the contract stays an implementation detail of the adapter.
type Session struct {
	ID        string
	CreatedAt time.Time
	ExpiresAt time.Time
	Employee  Employee
}

// Employee is the page's view of the signed-in employee.
type Employee struct {
	ID    string
	Name  string
	Email string
	Role  string
}
