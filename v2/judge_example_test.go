package schemaflux_test

import (
	"context"
	"fmt"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

// Validate decides in Go the rules it can decide, here an email address and a
// minimum age, and asks no model at all when nothing is left. The verdict is worked
// out here from the issues.
func ExampleValidate() {
	type signup struct {
		Email string `json:"email"`
		Age   int    `json:"age"`
	}
	var model schemafluxtest.Provider
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Validate(ctx, signup{Email: "ada", Age: 17}, schemaflux.ValidateOptions{
		FieldRules: map[string]string{"Email": "email", "Age": "min:18"},
	})
	fmt.Println(res.Verdict, res.DeterministicOnly, model.Calls(), err)
	for _, issue := range res.Issues {
		fmt.Println(issue.Subject+":", issue.Message)
	}
	// Output:
	// fail true 0 <nil>
	// Age: Age is below the minimum of 18
	// Email: Email is not a valid email address
}

// A rule Go cannot decide goes to the model, and the verdict still follows from
// the issues it lists and FailOn.
func ExampleValidate_rules() {
	var model schemafluxtest.Provider
	model.Reply(`{"errors":[],"warnings":[{"field":"total","message":"the lines add up to 40"}],"info":[],"confidence":0.8,"summary":"one warning"}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	order := map[string]any{"total": 42, "lines": []int{10, 30}}
	res, err := schemaflux.Validate(ctx, order, schemaflux.ValidateOptions{
		Rules:  "the total must equal the sum of the lines",
		FailOn: "warning",
	})
	fmt.Println(res.Verdict, len(res.Issues), res.Issues[0].Severity, err)
	// Output: fail 1 warning <nil>
}

// Verify checks a text with a model. The verdict is the worst of the checks that
// ran, worked out here: a false claim makes it fail whatever the model's summary
// says.
func ExampleVerify() {
	var model schemafluxtest.Provider
	model.Reply(`{"summary":"one claim is wrong","overall_confidence":0.9,"trust_score":0.2,` +
		`"claims":[{"claim":"The Earth is flat.","verdict":"false","confidence":0.97,"corrections":"It is an oblate spheroid.","evidence":["satellite imagery"],"reasoning":"measured"}],` +
		`"logic_issues":[],"consistency_issues":[]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Verify(ctx, "The Earth is flat.")
	fmt.Println(res.Verdict, len(res.Claims), res.ModelTrustScore, err)
	fmt.Println(res.Issues[0].Category, res.Issues[0].Suggestion)
	// Output:
	// fail 1 0.2 <nil>
	// fact:false It is an oblate spheroid.
}

// Audit fails when a policy the caller supplied is violated, whatever the
// severity the model gave the violation.
func ExampleAudit() {
	var model schemafluxtest.Provider
	model.Reply(`{"findings":[],"policies":{"p1":[{"category":"security","severity":0.2,"field":"password","issue":"stored in plain text","evidence":"hunter2","recommendation":"hash it"}]}}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	type customer struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	res, err := schemaflux.Audit(ctx, customer{Name: "Ada", Password: "hunter2"}, schemaflux.AuditOptions{
		Policies: []string{"Passwords must not be stored in plain text"},
	})
	fmt.Println(res.Verdict, res.ByPolicy["Passwords must not be stored in plain text"], err)
	for _, issue := range res.Issues {
		fmt.Println(issue.Rule, "|", issue.Subject)
	}
	// Output:
	// fail 1 <nil>
	// Passwords must not be stored in plain text | password
}

// Critique reviews a value against criteria. The verdict is worked out here: fail
// when an issue is critical, mixed when there are issues and none is, pass when
// there are none.
func ExampleCritique() {
	var model schemafluxtest.Provider
	model.Reply(`{"overall_score":0.6,"criteria_scores":{"clarity":0.5,"argument":0.7},` +
		`"issues":[{"criterion":"clarity","severity":"major","description":"The introduction does not say what the essay argues.","location":"first paragraph","suggestion":"State the thesis first.","fix":"Open with the claim."}],` +
		`"positives":[{"criterion":"argument","description":"Each point has a source."}],"summary":"A solid argument that is hard to find.","top_priorities":["State the thesis"]}`)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: &model})

	res, err := schemaflux.Critique(ctx, "An essay about tides.", []string{"clarity", "argument"})
	fmt.Println(res.Verdict, res.TopPriorities, err)
	fmt.Println(res.Issues[0].Subject, "-", res.Issues[0].Suggestion)
	// Output:
	// mixed [State the thesis] <nil>
	// clarity - Open with the claim.
}
