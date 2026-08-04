package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/joho/godotenv"
	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
)

// loadEnv loads environment variables from .env files
func loadEnv() {
	if err := godotenv.Load(); err == nil {
		return
	}
	dir, _ := os.Getwd()
	for i := 0; i < 3; i++ {
		envPath := filepath.Join(dir, ".env")
		if err := godotenv.Load(envPath); err == nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// ============================================================
// USE CASE 1: USPS Address Standardization
// ============================================================

// ShippingAddress from customer input
type ShippingAddress struct {
	Name       string `json:"name"`
	Street1    string `json:"street1"`
	Street2    string `json:"street2"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
	Country    string `json:"country"`
}

// ============================================================
// USE CASE 2: Financial Data ISO Conformance
// ============================================================

// FinancialTransaction from legacy system
type FinancialTransaction struct {
	TransactionID string  `json:"transaction_id"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	Timestamp     string  `json:"timestamp"`
	SenderIBAN    string  `json:"sender_iban"`
	ReceiverIBAN  string  `json:"receiver_iban"`
	Reference     string  `json:"reference"`
}

// ============================================================
// USE CASE 3: Healthcare HL7 FHIR Conformance
// ============================================================

// PatientRecord from EHR export
type PatientRecord struct {
	PatientID   string `json:"patient_id"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name"`
	DOB         string `json:"dob"`
	Gender      string `json:"gender"`
	SSN         string `json:"ssn"`
	Phone       string `json:"phone"`
	Diagnosis   string `json:"diagnosis"`
	DiagnosisAt string `json:"diagnosis_at"`
}

func main() {
	loadEnv()

	if err := schemaflux.InitWithEnv(); err != nil {
		fmt.Printf("Init failed: %v\n", err)
		return
	}

	fmt.Println("=== Conform Example ===")
	fmt.Println("Transforming data to match standards and schemas")

	// ============================================================
	// USE CASE 1: USPS Address Standardization
	// Scenario: Normalize customer-entered addresses for shipping labels
	// ============================================================
	fmt.Println("\n--- Use Case 1: USPS Address Standardization ---")

	rawAddress := ShippingAddress{
		Name:       "dr. john q. smith jr.",
		Street1:    "one-hundred twenty-three north main street, suite 400",
		Street2:    "",
		City:       "los angeles",
		State:      "california",
		PostalCode: "90210",
		Country:    "united states of america",
	}

	addrResult, err := schemaflux.Conform[ShippingAddress](rawAddress, "USPS", schemaflux.ConformOptions{
		Strict:       true,
		Intelligence: types.Smart,
		Steering:     "Apply full USPS Publication 28 standards: uppercase, abbreviated directionals (N, S, E, W), abbreviated suffixes (ST, AVE, BLVD), state abbreviations, and standard country code USA.",
	})
	if err != nil {
		fmt.Printf("USPS conform failed: %v\n", err)
	} else {
		fmt.Printf("Original:\n")
		fmt.Printf("  Name: %s\n", rawAddress.Name)
		fmt.Printf("  Street: %s\n", rawAddress.Street1)
		fmt.Printf("  City/State/ZIP: %s, %s %s\n", rawAddress.City, rawAddress.State, rawAddress.PostalCode)
		fmt.Printf("  Country: %s\n", rawAddress.Country)
		fmt.Printf("\nUSPS Conformed:\n")
		fmt.Printf("  Name: %s\n", addrResult.Conformed.Name)
		fmt.Printf("  Street1: %s\n", addrResult.Conformed.Street1)
		if addrResult.Conformed.Street2 != "" {
			fmt.Printf("  Street2: %s\n", addrResult.Conformed.Street2)
		}
		fmt.Printf("  City/State/ZIP: %s, %s %s\n",
			addrResult.Conformed.City, addrResult.Conformed.State, addrResult.Conformed.PostalCode)
		fmt.Printf("  Country: %s\n", addrResult.Conformed.Country)
		fmt.Printf("\nAdjustments (%d):\n", len(addrResult.Adjustments))
		for _, adj := range addrResult.Adjustments {
			fmt.Printf("  %s: %s\n", adj.Field, adj.Description)
		}
		fmt.Printf("Compliance: %.0f%%\n", addrResult.Compliance*100)
	}

	// ============================================================
	// USE CASE 2: Financial Data ISO Conformance
	// Scenario: Transform legacy payment data to ISO 20022 format
	// ============================================================
	fmt.Println("\n--- Use Case 2: Financial ISO 20022 Conformance ---")

	legacyTxn := FinancialTransaction{
		TransactionID: "PAY123456",
		Amount:        15750.00,
		Currency:      "dollars",
		Timestamp:     "12/15/2024 3:45 PM EST",
		SenderIBAN:    "de89370400440532013000",
		ReceiverIBAN:  "GB82 WEST 1234 5698 7654 32",
		Reference:     "invoice payment #INV-2024-0089",
	}

	finResult, err := schemaflux.Conform[FinancialTransaction](legacyTxn, "ISO 20022", schemaflux.ConformOptions{
		Intelligence: types.Smart,
		Steering:     "Apply ISO 20022 PAIN/PACS standards: ISO 4217 currency codes (USD not dollars), ISO 8601 timestamps in UTC, IBAN without spaces uppercase, structured reference max 35 chars.",
	})
	if err != nil {
		fmt.Printf("ISO 20022 conform failed: %v\n", err)
	} else {
		fmt.Printf("Original (Legacy):\n")
		fmt.Printf("  TxnID: %s\n", legacyTxn.TransactionID)
		fmt.Printf("  Amount: %.2f %s\n", legacyTxn.Amount, legacyTxn.Currency)
		fmt.Printf("  Timestamp: %s\n", legacyTxn.Timestamp)
		fmt.Printf("  Sender IBAN: %s\n", legacyTxn.SenderIBAN)
		fmt.Printf("  Receiver IBAN: %s\n", legacyTxn.ReceiverIBAN)
		fmt.Printf("  Reference: %s\n", legacyTxn.Reference)
		fmt.Printf("\nISO 20022 Conformed:\n")
		fmt.Printf("  TxnID: %s\n", finResult.Conformed.TransactionID)
		fmt.Printf("  Amount: %.2f %s\n", finResult.Conformed.Amount, finResult.Conformed.Currency)
		fmt.Printf("  Timestamp: %s\n", finResult.Conformed.Timestamp)
		fmt.Printf("  Sender IBAN: %s\n", finResult.Conformed.SenderIBAN)
		fmt.Printf("  Receiver IBAN: %s\n", finResult.Conformed.ReceiverIBAN)
		fmt.Printf("  Reference: %s\n", finResult.Conformed.Reference)
		fmt.Printf("\nAdjustments (%d):\n", len(finResult.Adjustments))
		for _, adj := range finResult.Adjustments {
			fmt.Printf("  %s: %s\n", adj.Field, adj.Description)
		}
		fmt.Printf("Compliance: %.0f%%\n", finResult.Compliance*100)
	}

	// ============================================================
	// USE CASE 3: Healthcare HL7 FHIR Conformance
	// Scenario: Transform EHR data to FHIR R4 Patient resource format
	// ============================================================
	fmt.Println("\n--- Use Case 3: Healthcare HL7 FHIR Conformance ---")

	patientData := PatientRecord{
		PatientID:   "PAT-00123456",
		FirstName:   "JANE",
		LastName:    "DOE",
		DOB:         "03/15/1985",
		Gender:      "F",
		SSN:         "123-45-6789",
		Phone:       "(555) 234-5678",
		Diagnosis:   "Type 2 Diabetes Mellitus",
		DiagnosisAt: "Jan 10, 2024",
	}

	fhirResult, err := schemaflux.Conform[PatientRecord](patientData, "HL7 FHIR R4", schemaflux.ConformOptions{
		Intelligence: types.Smart,
		Steering:     "Apply FHIR R4 standards: ISO 8601 dates, E.164 phone (+1...), FHIR gender codes (male/female/other/unknown), SSN should be masked to last 4 only (XXX-XX-####), ICD-10 diagnosis code format.",
	})
	if err != nil {
		fmt.Printf("FHIR conform failed: %v\n", err)
	} else {
		fmt.Printf("Original (EHR Export):\n")
		fmt.Printf("  Patient ID: %s\n", patientData.PatientID)
		fmt.Printf("  Name: %s %s\n", patientData.FirstName, patientData.LastName)
		fmt.Printf("  DOB: %s\n", patientData.DOB)
		fmt.Printf("  Gender: %s\n", patientData.Gender)
		fmt.Printf("  SSN: %s\n", patientData.SSN)
		fmt.Printf("  Phone: %s\n", patientData.Phone)
		fmt.Printf("  Diagnosis: %s @ %s\n", patientData.Diagnosis, patientData.DiagnosisAt)
		fmt.Printf("\nFHIR R4 Conformed:\n")
		fmt.Printf("  Patient ID: %s\n", fhirResult.Conformed.PatientID)
		fmt.Printf("  Name: %s %s\n", fhirResult.Conformed.FirstName, fhirResult.Conformed.LastName)
		fmt.Printf("  DOB: %s\n", fhirResult.Conformed.DOB)
		fmt.Printf("  Gender: %s\n", fhirResult.Conformed.Gender)
		fmt.Printf("  SSN: %s\n", fhirResult.Conformed.SSN)
		fmt.Printf("  Phone: %s\n", fhirResult.Conformed.Phone)
		fmt.Printf("  Diagnosis: %s @ %s\n",
			fhirResult.Conformed.Diagnosis, fhirResult.Conformed.DiagnosisAt)
		fmt.Printf("\nAdjustments (%d):\n", len(fhirResult.Adjustments))
		for _, adj := range fhirResult.Adjustments {
			fmt.Printf("  %s: %s\n", adj.Field, adj.Description)
		}
		if len(fhirResult.Violations) > 0 {
			fmt.Printf("Violations: %v\n", fhirResult.Violations)
		}
		fmt.Printf("Compliance: %.0f%%\n", fhirResult.Compliance*100)
	}

	fmt.Println("\n=== Conform Example Complete ===")
}
