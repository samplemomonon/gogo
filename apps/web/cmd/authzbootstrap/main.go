// Command authzbootstrap provisions the first tenant policy administrator.
// Run it with a unique operator PostgreSQL login and an approved change record.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	authpostgres "gogo/internal/features/auth/infrastructure/postgres"
)

func main() {
	tenant := flag.String("tenant", "", "tenant identifier")
	employee := flag.String("employee", "", "active employee identifier receiving policy administration")
	changeReference := flag.String("change-reference", "", "approved change or incident reference")
	flag.Parse()

	dsn := strings.TrimSpace(os.Getenv("AUTHZ_DATABASE_URL"))
	if dsn == "" {
		fatal("AUTHZ_DATABASE_URL is required")
	}
	if strings.TrimSpace(*tenant) == "" || strings.TrimSpace(*employee) == "" || strings.TrimSpace(*changeReference) == "" {
		fatal("-tenant, -employee, and -change-reference are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := authpostgres.Connect(ctx, dsn, 1, 1)
	if err != nil {
		fatal("connect to authorization database: %v", err)
	}
	defer pool.Close()
	if err := authpostgres.Migrate(ctx, pool); err != nil {
		fatal("apply auth migrations: %v", err)
	}
	if err := authpostgres.BootstrapPolicyAdmin(ctx, pool, *tenant, *employee, *changeReference); err != nil {
		fatal("bootstrap policy administrator: %v", err)
	}
	fmt.Printf("policy administrator bootstrapped: tenant=%s employee=%s change_reference=%s\n", strings.TrimSpace(*tenant), strings.TrimSpace(*employee), strings.TrimSpace(*changeReference))
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "authzbootstrap: "+format+"\n", args...)
	os.Exit(1)
}
