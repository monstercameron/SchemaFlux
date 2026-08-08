package main

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/joho/godotenv"
	"github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/types"
)

func loadEnv() {
	dir, _ := os.Getwd()
	for {
		if _, err := os.Stat(filepath.Join(dir, ".env")); err == nil {
			godotenv.Load(filepath.Join(dir, ".env"))
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

func main() {
	loadEnv()
	schemaflux.InitWithEnv()

	fmt.Println("=== NegotiateAdversarial Example ===")
	fmt.Println("Two-party adversarial negotiation: Ours vs Theirs with leverage")
	fmt.Println()

	runSalaryNegotiation()
	time.Sleep(2 * time.Second)
	runVendorContract()
	time.Sleep(2 * time.Second)
	runAcquisition()

	fmt.Println("\n=== NegotiateAdversarial Example Complete ===")
}

func runSalaryNegotiation() {
	fmt.Println("--- Use Case 1: Salary Negotiation (Candidate vs Company) ---")

	type SalaryTerms struct {
		BaseSalary int `json:"base_salary"`
		RemoteDays int `json:"remote_days"`
		Bonus      int `json:"bonus"`
	}

	ctx := schemaflux.AdversarialContext[SalaryTerms]{
		Ours: schemaflux.AdversarialPosition[SalaryTerms]{
			Position: SalaryTerms{BaseSalary: 160000, RemoteDays: 5, Bonus: 20000},
		},
		Theirs: schemaflux.AdversarialPosition[SalaryTerms]{
			Position: SalaryTerms{BaseSalary: 130000, RemoteDays: 2, Bonus: 5000},
		},
		OurLeverage: "strong",
	}

	fmt.Println("INPUT: AdversarialContext[SalaryTerms]{")
	fmt.Printf("  Ours:   {Position: {Salary: $%d, Remote: %d, Bonus: $%d}},\n",
		ctx.Ours.Position.BaseSalary, ctx.Ours.Position.RemoteDays, ctx.Ours.Position.Bonus)
	fmt.Printf("  Theirs: {Position: {Salary: $%d, Remote: %d, Bonus: $%d}},\n",
		ctx.Theirs.Position.BaseSalary, ctx.Theirs.Position.RemoteDays, ctx.Theirs.Position.Bonus)
	fmt.Printf("  OurLeverage: %q,\n", ctx.OurLeverage)
	fmt.Println("}")
	fmt.Println("OPTIONS: Steering: \"They definitely don't want RTO but salary might be moveable\"")

	result, err := schemaflux.SettleAdversarial[SalaryTerms](ctx, schemaflux.AdversarialOptions{
		Intelligence: types.Smart,
		Steering:     "They definitely don't want RTO so hold firm on remote days. Salary is more flexible for them.",
	})
	if err != nil {
		fmt.Printf("Error: %v\n\n", err)
		return
	}

	fmt.Println("OUTPUT: AdversarialResult[SalaryTerms]{")
	fmt.Printf("  Deal: {Salary: $%d, Remote: %d, Bonus: $%d},\n",
		result.Deal.BaseSalary, result.Deal.RemoteDays, result.Deal.Bonus)
	fmt.Printf("  DealReached: %v,\n", result.DealReached)
	fmt.Printf("  WhoConcededMore: %q,\n", result.WhoConcededMore)
	if len(result.TermMovements) > 0 {
		fmt.Println("  TermMovements: []TermMovement{")
		for _, tm := range result.TermMovements {
			fmt.Printf("    {Term: %q, OurAsk: %v, TheirOffer: %v, Final: %v, Movement: %q},\n",
				tm.Term, tm.OurAsk, tm.TheirOffer, tm.FinalValue, tm.Movement)
		}
		fmt.Println("  },")
	}
	fmt.Printf("  OurSatisfaction: %.2f, TheirSatisfaction: %.2f,\n",
		result.OurSatisfaction, result.TheirSatisfaction)
	fmt.Println("}")
	fmt.Println()
}

func runVendorContract() {
	fmt.Println("--- Use Case 2: Vendor Contract (Buyer vs Seller) ---")

	type ContractTerms struct {
		Price    float64 `json:"price_per_unit"`
		Quantity int     `json:"quantity"`
		Terms    int     `json:"payment_days"`
	}

	ctx := schemaflux.AdversarialContext[ContractTerms]{
		Ours: schemaflux.AdversarialPosition[ContractTerms]{
			Position: ContractTerms{Price: 45, Quantity: 500, Terms: 60},
		},
		Theirs: schemaflux.AdversarialPosition[ContractTerms]{
			Position: ContractTerms{Price: 65, Quantity: 1000, Terms: 30},
		},
		OurLeverage: "strong",
	}

	fmt.Println("INPUT: AdversarialContext[ContractTerms]{")
	fmt.Printf("  Ours:   {Position: {Price: $%.0f, Qty: %d, Terms: %d days}},\n",
		ctx.Ours.Position.Price, ctx.Ours.Position.Quantity, ctx.Ours.Position.Terms)
	fmt.Printf("  Theirs: {Position: {Price: $%.0f, Qty: %d, Terms: %d days}},\n",
		ctx.Theirs.Position.Price, ctx.Theirs.Position.Quantity, ctx.Theirs.Position.Terms)
	fmt.Printf("  OurLeverage: %q,\n", ctx.OurLeverage)
	fmt.Println("}")
	fmt.Println("OPTIONS: Steering: \"Seller is desperate to close Q4, price is negotiable\"")

	result, err := schemaflux.SettleAdversarial[ContractTerms](ctx, schemaflux.AdversarialOptions{
		Intelligence: types.Smart,
		Steering:     "Seller is desperate to close before Q4 ends. Price is very negotiable. Quantity less so.",
	})
	if err != nil {
		fmt.Printf("Error: %v\n\n", err)
		return
	}

	fmt.Println("OUTPUT: AdversarialResult[ContractTerms]{")
	fmt.Printf("  Deal: {Price: $%.2f, Qty: %d, Terms: %d days},\n",
		result.Deal.Price, result.Deal.Quantity, result.Deal.Terms)
	fmt.Printf("  DealReached: %v,\n", result.DealReached)
	fmt.Printf("  WhoConcededMore: %q,\n", result.WhoConcededMore)
	if len(result.TermMovements) > 0 {
		fmt.Println("  TermMovements: []TermMovement{")
		for _, tm := range result.TermMovements {
			fmt.Printf("    {Term: %q, OurAsk: %v, TheirOffer: %v, Final: %v, Movement: %q},\n",
				tm.Term, tm.OurAsk, tm.TheirOffer, tm.FinalValue, tm.Movement)
		}
		fmt.Println("  },")
	}
	fmt.Printf("  OurSatisfaction: %.2f, TheirSatisfaction: %.2f,\n",
		result.OurSatisfaction, result.TheirSatisfaction)
	fmt.Println("}")
	fmt.Println()
}

func runAcquisition() {
	fmt.Println("--- Use Case 3: M&A Acquisition (Acquirer vs Target) ---")

	type AcquisitionTerms struct {
		Valuation int `json:"valuation_millions"`
		Earnout   int `json:"earnout_percent"`
		Retention int `json:"retention_months"`
	}

	// Note: Target has leverage due to competing bidders
	ctx := schemaflux.AdversarialContext[AcquisitionTerms]{
		Ours: schemaflux.AdversarialPosition[AcquisitionTerms]{
			Position: AcquisitionTerms{Valuation: 80, Earnout: 30, Retention: 24},
		},
		Theirs: schemaflux.AdversarialPosition[AcquisitionTerms]{
			Position: AcquisitionTerms{Valuation: 120, Earnout: 10, Retention: 6},
		},
		OurLeverage:  "weak",
		Relationship: "competitive",
	}

	fmt.Println("INPUT: AdversarialContext[AcquisitionTerms]{")
	fmt.Printf("  Ours:   {Position: {Val: $%dM, Earnout: %d%%, Retention: %d mo}},\n",
		ctx.Ours.Position.Valuation, ctx.Ours.Position.Earnout, ctx.Ours.Position.Retention)
	fmt.Printf("  Theirs: {Position: {Val: $%dM, Earnout: %d%%, Retention: %d mo}},\n",
		ctx.Theirs.Position.Valuation, ctx.Theirs.Position.Earnout, ctx.Theirs.Position.Retention)
	fmt.Printf("  OurLeverage: %q, Relationship: %q,\n", ctx.OurLeverage, ctx.Relationship)
	fmt.Println("}")

	result, err := schemaflux.SettleAdversarial[AcquisitionTerms](ctx, schemaflux.AdversarialOptions{
		Intelligence: types.Smart,
		Steering:     "Target has 3 competing bidders, so they have the power.",
	})
	if err != nil {
		fmt.Printf("Error: %v\n\n", err)
		return
	}

	fmt.Println("OUTPUT: AdversarialResult[AcquisitionTerms]{")
	fmt.Printf("  Deal: {Val: $%dM, Earnout: %d%%, Retention: %d months},\n",
		result.Deal.Valuation, result.Deal.Earnout, result.Deal.Retention)
	fmt.Printf("  DealReached: %v,\n", result.DealReached)
	fmt.Printf("  WhoConcededMore: %q,\n", result.WhoConcededMore)
	if len(result.TermMovements) > 0 {
		fmt.Println("  TermMovements: []TermMovement{")
		for _, tm := range result.TermMovements {
			fmt.Printf("    {Term: %q, OurAsk: %v, TheirOffer: %v, Final: %v, Movement: %q},\n",
				tm.Term, tm.OurAsk, tm.TheirOffer, tm.FinalValue, tm.Movement)
		}
		fmt.Println("  },")
	}
	fmt.Printf("  OurSatisfaction: %.2f, TheirSatisfaction: %.2f,\n",
		result.OurSatisfaction, result.TheirSatisfaction)
	fmt.Println("}")
}
