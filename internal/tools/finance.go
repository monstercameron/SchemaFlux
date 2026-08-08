package tools

import (
	"context"
	"fmt"
	"math"
)

// TaxTool calculates various types of taxes
var TaxTool = &Tool{
	Name:        "tax",
	Description: "Calculate various types of taxes including sales tax, VAT, and income tax.",
	Category:    CategoryFinance,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"type":   EnumParam("Type of tax calculation", []string{"sales", "vat", "income", "tip"}),
		"amount": NumberParam("Amount to calculate tax on"),
		"rate":   NumberParam("Tax rate as percentage (e.g., 8.25 for 8.25%)"),
	}, []string{"type", "amount", "rate"}),
	Execute: executeTax,
}

func executeTax(ctx context.Context, params map[string]any) (Result, error) {
	taxType, _ := params["type"].(string)
	amount, _ := params["amount"].(float64)
	rate, _ := params["rate"].(float64)

	if amount <= 0 {
		return ErrorResultFromError(fmt.Errorf("amount must be positive")), nil
	}
	if rate < 0 || rate > 100 {
		return ErrorResultFromError(fmt.Errorf("rate must be between 0 and 100")), nil
	}

	taxAmount := amount * (rate / 100)
	total := amount + taxAmount

	result := map[string]any{
		"type":       taxType,
		"amount":     amount,
		"rate":       rate,
		"tax_amount": math.Round(taxAmount*100) / 100,
		"total":      math.Round(total*100) / 100,
	}

	switch taxType {
	case "sales":
		result["description"] = fmt.Sprintf("Sales tax of %.2f%% on $%.2f", rate, amount)
	case "vat":
		result["description"] = fmt.Sprintf("VAT of %.2f%% on $%.2f", rate, amount)
	case "income":
		result["description"] = fmt.Sprintf("Income tax of %.2f%% on $%.2f", rate, amount)
	case "tip":
		result["description"] = fmt.Sprintf("%.2f%% tip on $%.2f", rate, amount)
	}

	return NewResultWithMeta(result, map[string]any{"type": taxType}), nil
}

// InterestTool calculates various types of interest
var InterestTool = &Tool{
	Name:        "interest",
	Description: "Calculate simple, compound, and loan interest.",
	Category:    CategoryFinance,
	Parameters: ObjectSchema(map[string]ParameterSchema{
		"type":      EnumParam("Type of interest calculation", []string{"simple", "compound", "loan", "savings", "mortgage"}),
		"principal": NumberParam("Principal amount"),
		"rate":      NumberParam("Annual interest rate as percentage"),
		"time":      NumberParam("Time period in years"),
		"compounds": NumberParam("Compounds per year (for compound interest, default: 12)"),
		"payment":   NumberParam("Monthly payment (for loan calculations)"),
	}, []string{"type", "principal", "rate", "time"}),
	Execute: executeInterest,
}

func executeInterest(ctx context.Context, params map[string]any) (Result, error) {
	calcType, _ := params["type"].(string)
	principal, _ := params["principal"].(float64)
	rate, _ := params["rate"].(float64)
	time, _ := params["time"].(float64)
	compounds := 12.0
	if c, ok := params["compounds"].(float64); ok && c > 0 {
		compounds = c
	}

	if principal <= 0 {
		return ErrorResultFromError(fmt.Errorf("principal must be positive")), nil
	}
	if rate < 0 {
		return ErrorResultFromError(fmt.Errorf("rate must be non-negative")), nil
	}
	if time <= 0 {
		return ErrorResultFromError(fmt.Errorf("time must be positive")), nil
	}

	r := rate / 100 // Convert percentage to decimal
	var result map[string]any

	switch calcType {
	case "simple":
		interest := principal * r * time
		total := principal + interest
		result = map[string]any{
			"type":      "simple",
			"principal": principal,
			"rate":      rate,
			"time":      time,
			"interest":  math.Round(interest*100) / 100,
			"total":     math.Round(total*100) / 100,
		}

	case "compound":
		// A = P(1 + r/n)^(nt)
		total := principal * math.Pow(1+r/compounds, compounds*time)
		interest := total - principal
		result = map[string]any{
			"type":             "compound",
			"principal":        principal,
			"rate":             rate,
			"time":             time,
			"compounds_yearly": compounds,
			"interest":         math.Round(interest*100) / 100,
			"total":            math.Round(total*100) / 100,
		}

	case "loan", "mortgage":
		// Monthly payment = P * [r(1+r)^n] / [(1+r)^n - 1]
		monthlyRate := r / 12
		numPayments := time * 12
		if monthlyRate == 0 {
			// No interest loan
			monthlyPayment := principal / numPayments
			result = map[string]any{
				"type":            calcType,
				"principal":       principal,
				"rate":            rate,
				"time":            time,
				"monthly_payment": math.Round(monthlyPayment*100) / 100,
				"total_payment":   principal,
				"total_interest":  0.0,
			}
		} else {
			monthlyPayment := principal * (monthlyRate * math.Pow(1+monthlyRate, numPayments)) / (math.Pow(1+monthlyRate, numPayments) - 1)
			totalPayment := monthlyPayment * numPayments
			totalInterest := totalPayment - principal
			result = map[string]any{
				"type":            calcType,
				"principal":       principal,
				"rate":            rate,
				"time":            time,
				"monthly_payment": math.Round(monthlyPayment*100) / 100,
				"total_payment":   math.Round(totalPayment*100) / 100,
				"total_interest":  math.Round(totalInterest*100) / 100,
			}
		}

	case "savings":
		// Future value with regular deposits
		// FV = P*(1+r/n)^(nt) + PMT*[((1+r/n)^(nt) - 1) / (r/n)]
		payment, _ := params["payment"].(float64)
		if payment == 0 {
			payment = 0 // No regular deposits
		}
		periodicRate := r / compounds
		totalPeriods := compounds * time
		futureValue := principal * math.Pow(1+periodicRate, totalPeriods)
		if periodicRate > 0 && payment > 0 {
			futureValue += payment * (math.Pow(1+periodicRate, totalPeriods) - 1) / periodicRate
		}
		totalDeposits := principal + payment*totalPeriods
		earnings := futureValue - totalDeposits
		result = map[string]any{
			"type":            "savings",
			"principal":       principal,
			"rate":            rate,
			"time":            time,
			"monthly_deposit": payment,
			"total_deposits":  math.Round(totalDeposits*100) / 100,
			"earnings":        math.Round(earnings*100) / 100,
			"future_value":    math.Round(futureValue*100) / 100,
		}

	default:
		return ErrorResultFromError(fmt.Errorf("unknown calculation type: %s", calcType)), nil
	}

	return NewResultWithMeta(result, map[string]any{"type": calcType}), nil
}

func init() {
	mustRegister(TaxTool)
	mustRegister(InterestTool)
}
