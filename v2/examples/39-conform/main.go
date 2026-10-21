package main

import (
	"context"
	"fmt"
	"io"
	"os"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/exp"
)

type address struct {
	Name       string `json:"name"`
	Street     string `json:"street1"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}
type transaction struct {
	ID         string  `json:"transaction_id"`
	Amount     float64 `json:"amount"`
	Currency   string  `json:"currency"`
	Timestamp  string  `json:"timestamp"`
	SenderIBAN string  `json:"sender_iban"`
}
type patient struct {
	ID     string `json:"patient_id"`
	DOB    string `json:"dob"`
	Gender string `json:"gender"`
	SSN    string `json:"ssn"`
	Phone  string `json:"phone"`
}

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Conform example: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, out io.Writer) error {
	fmt.Fprintln(out, "=== Conform Example ===")
	mail := address{Name: "dr. john q. smith jr.", Street: "one-hundred twenty-three north main street", City: "los angeles", State: "california", PostalCode: "90210", Country: "united states"}
	usps, err := exp.Conform(ctx, mail, "USPS", exp.ConformOptions{
		Strict: true, Options: schemaflux.Options{Tier: schemaflux.TierSmart,
			Steering: "Apply USPS Publication 28 abbreviations and uppercase postal fields."},
	})
	if err != nil {
		return fmt.Errorf("conform address: %w", err)
	}
	fmt.Fprintf(out, "USPS address: %s, %s %s; model compliance %.0f%%\n", usps.Conformed.Street, usps.Conformed.State, usps.Conformed.PostalCode, usps.Compliance*100)

	payment := transaction{ID: "PAY123456", Amount: 15750, Currency: "dollars", Timestamp: "12/15/2024 3:45 PM EST", SenderIBAN: "de89370400440532013000"}
	iso, err := exp.Conform(ctx, payment, "ISO 20022", exp.ConformOptions{Options: schemaflux.Options{
		Tier: schemaflux.TierSmart, Steering: "Use ISO 4217 currency codes, ISO 8601 timestamps and uppercase IBAN."},
	})
	if err != nil {
		return fmt.Errorf("conform transaction: %w", err)
	}
	fmt.Fprintf(out, "ISO 20022 payment: %s %.2f %s at %s\n", iso.Conformed.ID, iso.Conformed.Amount, iso.Conformed.Currency, iso.Conformed.Timestamp)

	record := patient{ID: "PAT-00123456", DOB: "03/15/1985", Gender: "F", SSN: "123-45-6789", Phone: "(555) 234-5678"}
	fhir, err := exp.Conform(ctx, record, "HL7 FHIR R4", exp.ConformOptions{Options: schemaflux.Options{
		Tier: schemaflux.TierSmart, Steering: "Use ISO 8601 dates, FHIR gender codes, E.164 phone and a masked SSN."},
	})
	if err != nil {
		return fmt.Errorf("conform patient: %w", err)
	}
	// The example reports only non-sensitive fields; the patient value contains
	// the original identifier and must not be sent to an application log.
	fmt.Fprintf(out, "FHIR patient: %s; birth date %s; gender %s\n", fhir.Conformed.ID, fhir.Conformed.DOB, fhir.Conformed.Gender)
	fmt.Fprintln(out, "=== Conform Example Complete ===")
	return nil
}
