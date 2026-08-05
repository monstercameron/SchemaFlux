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
// USE CASE 1: Customer Lifetime Value Features
// ============================================================

// CustomerActivity - basic activity data
type CustomerActivity struct {
	CustomerID     string  `json:"customer_id"`
	SignupDate     string  `json:"signup_date"`
	TotalOrders    int     `json:"total_orders"`
	TotalSpend     float64 `json:"total_spend"`
	LastOrderDate  string  `json:"last_order_date"`
	ReturnCount    int     `json:"return_count"`
	SupportTickets int     `json:"support_tickets"`
}

// CustomerLTVFeatures - derived ML features
type CustomerLTVFeatures struct {
	CustomerID      string  `json:"customer_id"`
	TenureDays      int     `json:"tenure_days"`
	OrderFrequency  float64 `json:"order_frequency"`
	AvgOrderValue   float64 `json:"avg_order_value"`
	DaysSinceOrder  int     `json:"days_since_order"`
	ReturnRate      float64 `json:"return_rate"`
	ChurnRisk       string  `json:"churn_risk"`
	CustomerSegment string  `json:"customer_segment"`
}

// ============================================================
// USE CASE 2: Real Estate Property Features
// ============================================================

// PropertyListing - basic listing data
type PropertyListing struct {
	Address   string  `json:"address"`
	City      string  `json:"city"`
	ZipCode   string  `json:"zip_code"`
	SqFt      int     `json:"sq_ft"`
	Bedrooms  int     `json:"bedrooms"`
	Bathrooms float64 `json:"bathrooms"`
	YearBuilt int     `json:"year_built"`
	ListPrice int     `json:"list_price"`
	LotAcres  float64 `json:"lot_acres"`
	HasGarage bool    `json:"has_garage"`
	HasPool   bool    `json:"has_pool"`
}

// PropertyFeatures - derived features for pricing model
type PropertyFeatures struct {
	Address         string  `json:"address"`
	PricePerSqFt    float64 `json:"price_per_sq_ft"`
	PropertyAge     int     `json:"property_age"`
	BedroomRatio    float64 `json:"bedroom_ratio"`
	PropertyClass   string  `json:"property_class"`
	PriceTier       string  `json:"price_tier"`
	InvestmentScore float64 `json:"investment_score"`
}

// ============================================================
// USE CASE 3: Resume Skill Extraction
// ============================================================

// ResumeBasic - parsed resume text
type ResumeBasic struct {
	Name       string `json:"name"`
	Title      string `json:"current_title"`
	Experience string `json:"experience_summary"`
	Education  string `json:"education"`
	YearsExp   int    `json:"years_experience"`
	Industry   string `json:"industry"`
}

// CandidateProfile - derived hiring features
type CandidateProfile struct {
	Name            string   `json:"name"`
	SeniorityLevel  string   `json:"seniority_level"`
	SkillCategories []string `json:"skill_categories"`
	LeadershipScore float64  `json:"leadership_score"`
	TechnicalDepth  string   `json:"technical_depth"`
	SalaryRange     string   `json:"expected_salary_range"`
	FitScore        float64  `json:"fit_score"`
}

func main() {
	loadEnv()

	if err := schemaflux.InitWithEnv(); err != nil {
		fmt.Printf("Init failed: %v\n", err)
		return
	}

	fmt.Println("=== Derive Example ===")
	fmt.Println("Inferring new structured data from existing data")

	// ============================================================
	// USE CASE 1: Customer Lifetime Value Features
	// Scenario: Derive ML features for churn prediction model
	// ============================================================
	fmt.Println("\n--- Use Case 1: Customer LTV Features ---")

	customer := CustomerActivity{
		CustomerID:     "CUST-8847",
		SignupDate:     "2021-03-15",
		TotalOrders:    47,
		TotalSpend:     8542.50,
		LastOrderDate:  "2024-11-28",
		ReturnCount:    3,
		SupportTickets: 2,
	}

	custResult, err := schemaflux.Derive[CustomerActivity, CustomerLTVFeatures](customer, schemaflux.DeriveOptions{
		Intelligence: types.Smart,
		Steering:     "Use today's date as 2024-12-05. Churn risk: low/medium/high based on days since order and frequency. Segments: VIP (>$5k spend), Regular, At-Risk.",
	})
	if err != nil {
		fmt.Printf("Customer derivation failed: %v\n", err)
	} else {
		fmt.Printf("Input: CustomerID=%s, Orders=%d, Spend=$%.2f\n",
			customer.CustomerID, customer.TotalOrders, customer.TotalSpend)
		fmt.Printf("\nDerived LTV Features:\n")
		fmt.Printf("  Tenure Days: %d\n", custResult.Derived.TenureDays)
		fmt.Printf("  Order Frequency: %.2f orders/month\n", custResult.Derived.OrderFrequency)
		fmt.Printf("  Avg Order Value: $%.2f\n", custResult.Derived.AvgOrderValue)
		fmt.Printf("  Days Since Order: %d\n", custResult.Derived.DaysSinceOrder)
		fmt.Printf("  Return Rate: %.1f%%\n", custResult.Derived.ReturnRate*100)
		fmt.Printf("  Churn Risk: %s\n", custResult.Derived.ChurnRisk)
		fmt.Printf("  Customer Segment: %s\n", custResult.Derived.CustomerSegment)
		fmt.Printf("\nOverall ModelConfidence: %.0f%%\n", custResult.ModelOverallConfidence*100)
	}

	// ============================================================
	// USE CASE 2: Real Estate Property Features
	// Scenario: Derive pricing model features from listing
	// ============================================================
	fmt.Println("\n--- Use Case 2: Real Estate Property Features ---")

	property := PropertyListing{
		Address:   "1842 Maple Drive",
		City:      "Austin",
		ZipCode:   "78704",
		SqFt:      2850,
		Bedrooms:  4,
		Bathrooms: 3.5,
		YearBuilt: 2018,
		ListPrice: 875000,
		LotAcres:  0.25,
		HasGarage: true,
		HasPool:   true,
	}

	propResult, err := schemaflux.Derive[PropertyListing, PropertyFeatures](property, schemaflux.DeriveOptions{
		Intelligence: types.Smart,
		Steering:     "Property class: Starter/Mid-Range/Luxury. Price tier: Budget/Moderate/Premium/Ultra-Premium. Investment score 0-1 based on price/sqft ratio, age, and amenities.",
	})
	if err != nil {
		fmt.Printf("Property derivation failed: %v\n", err)
	} else {
		fmt.Printf("Input: %s, %d sqft, $%d\n",
			property.Address, property.SqFt, property.ListPrice)
		fmt.Printf("\nDerived Property Features:\n")
		fmt.Printf("  Price Per SqFt: $%.2f\n", propResult.Derived.PricePerSqFt)
		fmt.Printf("  Property Age: %d years\n", propResult.Derived.PropertyAge)
		fmt.Printf("  Bedroom Ratio: %.2f beds/1000sqft\n", propResult.Derived.BedroomRatio)
		fmt.Printf("  Property Class: %s\n", propResult.Derived.PropertyClass)
		fmt.Printf("  Price Tier: %s\n", propResult.Derived.PriceTier)
		fmt.Printf("  Investment Score: %.2f\n", propResult.Derived.InvestmentScore)
		fmt.Printf("\nDerivation Methods:\n")
		for _, d := range propResult.Derivations {
			if d.Field == "investment_score" || d.Field == "property_class" {
				fmt.Printf("  %s: %s\n", d.Field, d.Method)
			}
		}
		fmt.Printf("ModelConfidence: %.0f%%\n", propResult.ModelOverallConfidence*100)
	}

	// ============================================================
	// USE CASE 3: Resume Skill Extraction
	// Scenario: Derive hiring features from resume data
	// ============================================================
	fmt.Println("\n--- Use Case 3: Resume Skill Extraction ---")

	resume := ResumeBasic{
		Name:       "Alex Rivera",
		Title:      "Engineering Manager",
		Experience: "Led team of 12 engineers building distributed systems. Previously Staff Engineer at FAANG. Built ML pipelines processing 10M events/day.",
		Education:  "MS Computer Science, Stanford",
		YearsExp:   11,
		Industry:   "Technology",
	}

	resumeResult, err := schemaflux.Derive[ResumeBasic, CandidateProfile](resume, schemaflux.DeriveOptions{
		Intelligence: types.Smart,
		Steering:     "Seniority: Junior/Mid/Senior/Staff/Principal/Director. Technical depth: Generalist/Specialist/Deep-Specialist. Salary range should reflect Bay Area tech market for role level.",
	})
	if err != nil {
		fmt.Printf("Resume derivation failed: %v\n", err)
	} else {
		fmt.Printf("Input: %s - %s (%d years)\n",
			resume.Name, resume.Title, resume.YearsExp)
		fmt.Printf("\nDerived Candidate Profile:\n")
		fmt.Printf("  Seniority Level: %s\n", resumeResult.Derived.SeniorityLevel)
		fmt.Printf("  Skill Categories: %v\n", resumeResult.Derived.SkillCategories)
		fmt.Printf("  Leadership Score: %.2f\n", resumeResult.Derived.LeadershipScore)
		fmt.Printf("  Technical Depth: %s\n", resumeResult.Derived.TechnicalDepth)
		fmt.Printf("  Expected Salary: %s\n", resumeResult.Derived.SalaryRange)
		fmt.Printf("  Fit Score: %.2f\n", resumeResult.Derived.FitScore)
		fmt.Printf("\nKey Derivations:\n")
		for _, d := range resumeResult.Derivations {
			if d.Reasoning != "" {
				fmt.Printf("  %s: %s\n", d.Field, d.Reasoning)
			}
		}
		fmt.Printf("ModelConfidence: %.0f%%\n", resumeResult.ModelOverallConfidence*100)
	}

	fmt.Println("\n=== Derive Example Complete ===")
}
