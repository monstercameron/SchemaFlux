package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type customer struct {
	ID      string `json:"customer_id"`
	Name    string `json:"name"`
	Email   string `json:"email"`
	Address string `json:"address"`
	Status  string `json:"status"`
}
type product struct {
	SKU   string  `json:"sku"`
	Name  string  `json:"name"`
	Price float64 `json:"price"`
	Stock int     `json:"stock"`
}
type employee struct {
	ID         string `json:"employee_id"`
	Name       string `json:"full_name"`
	Department string `json:"department"`
	Title      string `json:"title"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Resolve example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Resolve Example ===")
	fmt.Fprintln(out, "--- Customer record deduplication ---")
	customers := []customer{
		{ID: "CUST-5521", Name: "John Smith", Email: "john.smith@email.com", Address: "123 Main St", Status: "active"},
		{ID: "CUST-5521", Name: "John A. Smith", Email: "jsmith@work.com", Address: "123 Main Street, Boston", Status: "active"},
		{ID: "CUST-5521", Name: "John Smith", Email: "john.smith@email.com", Address: "456 Oak Ave", Status: "inactive"},
	}
	c, err := exp.Resolve(ctx, customers, exp.ResolveOptions{
		Strategy: "most-complete", Options: schemaflux.Options{Tier: schemaflux.TierSmart,
			Steering: "Prefer recent contact details and active status."},
	})
	if err != nil {
		return fmt.Errorf("resolve customer: %w", err)
	}
	fmt.Fprintf(out, "Customer: %s; address: %s; conflicts: %d\n", c.Resolved.Name, c.Resolved.Address, len(c.Conflicts))

	fmt.Fprintln(out, "--- Product catalog consolidation ---")
	products := []product{
		{SKU: "PROD-7890", Name: "Wireless Headphones", Price: 79.99, Stock: 150},
		{SKU: "PROD-7890", Name: "Wireless Headphones Pro", Price: 89.99, Stock: 75},
		{SKU: "PROD-7890", Name: "BT Headphones", Price: 79.99, Stock: 200},
	}
	p, err := exp.Resolve(ctx, products, exp.ResolveOptions{
		Strategy: "most-complete", FieldPriorities: map[string]int{"price": 0, "stock": 2},
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("resolve product: %w", err)
	}
	fmt.Fprintf(out, "Product: %s; price: $%.2f; stock: %d\n", p.Resolved.Name, p.Resolved.Price, p.Resolved.Stock)

	fmt.Fprintln(out, "--- Employee record reconciliation ---")
	employees := []employee{
		{ID: "EMP-1234", Name: "Sarah Johnson", Department: "Engineering", Title: "Senior Software Engineer"},
		{ID: "EMP-1234", Name: "Sarah M. Johnson", Department: "Platform Engineering", Title: "Staff Engineer"},
		{ID: "EMP-1234", Name: "Sarah Johnson", Department: "Engineering", Title: "Software Engineer"},
	}
	e, err := exp.Resolve(ctx, employees, exp.ResolveOptions{
		Strategy: "authoritative", AuthoritativeSource: 1,
		Options: schemaflux.Options{Tier: schemaflux.TierSmart},
	})
	if err != nil {
		return fmt.Errorf("resolve employee: %w", err)
	}
	fmt.Fprintf(out, "Employee: %s; department: %s; title: %s\n", e.Resolved.Name, e.Resolved.Department, e.Resolved.Title)
	fmt.Fprintln(out, "=== Resolve Example Complete ===")
	return nil
}
