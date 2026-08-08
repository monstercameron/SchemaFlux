package tests

import (
	"fmt"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux"
	"github.com/monstercameron/schemaflux/internal/testfixtures"
)

type internalUser struct {
	ID           string `json:"id"`
	Email        string `json:"email"`
	PasswordHash string `json:"password_hash"`
	SSN          string `json:"ssn"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name"`
}

type publicProfile struct {
	UserID      string `json:"user_id"`
	DisplayName string `json:"display_name"`
}

const (
	testSSN  = "123-45-6789"
	testHash = "$2y$10$abcdefghijklmnopqrstuv"
)

func sampleUser() internalUser {
	return internalUser{
		ID:           "u1",
		Email:        "ada@example.com",
		PasswordHash: testHash,
		SSN:          testSSN,
		FirstName:    "Ada",
		LastName:     "Lovelace",
	}
}

// Exclude was a prompt hint: the value was serialised and sent, and the model
// was asked not to look at it. This checks the bytes that actually reach the
// provider.
func TestIntegrationProjectWithholdsExcludedValues(t *testing.T) {
	cases := []struct {
		name    string
		exclude []string
		absent  []string
		present []string
	}{
		{"one_field", []string{"ssn"}, []string{testSSN}, []string{"ada@example.com", testHash}},
		{"two_fields", []string{"ssn", "password_hash"}, []string{testSSN, testHash}, []string{"ada@example.com"}},
		{"case_insensitive", []string{"SSN"}, []string{testSSN}, []string{"ada@example.com"}},
		{"unknown_field_is_harmless", []string{"nope"}, nil, []string{testSSN, "ada@example.com"}},
		{"no_exclusions", nil, nil, []string{testSSN, testHash, "ada@example.com"}},
		{"everything_sensitive", []string{"ssn", "password_hash", "email"}, []string{testSSN, testHash, "ada@example.com"}, []string{"Ada"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := testfixtures.WithScriptedProvider(t, `{"projected":{"user_id":"u1","display_name":"Ada Lovelace"},"confidence":0.9}`, nil)

			if _, err := schemaflux.Project[internalUser, publicProfile](
				sampleUser(), schemaflux.ProjectOptions{Exclude: tc.exclude}); err != nil {
				t.Fatalf("Project: %v", err)
			}
			if len(provider.Requests()) == 0 {
				t.Fatal("no request was recorded")
			}

			var sent strings.Builder
			for _, request := range provider.Requests() {
				sent.WriteString(renderRequest(request))
			}
			body := sent.String()

			for _, secret := range tc.absent {
				if strings.Contains(body, secret) {
					t.Errorf("the request carries the excluded value %q", secret)
				}
			}
			for _, expected := range tc.present {
				if !strings.Contains(body, expected) {
					t.Errorf("the request is missing %q, which was not excluded", expected)
				}
			}
		})
	}
}

// The projection itself still works, and reports what it mapped.
func TestIntegrationProjectProducesTheProfile(t *testing.T) {
	testfixtures.WithScriptedProvider(t, `{"projected":{"user_id":"u1","display_name":"Ada Lovelace"},
	                          "mappings":[{"source_field":"id","target_field":"user_id","method":"rename"}],
	                          "lost":["email"],"confidence":0.91}`, nil)

	result, err := schemaflux.Project[internalUser, publicProfile](
		sampleUser(), schemaflux.ProjectOptions{Exclude: []string{"ssn", "password_hash"}})
	if err != nil {
		t.Fatalf("Project: %v", err)
	}
	if result.Projected.UserID != "u1" || result.Projected.DisplayName != "Ada Lovelace" {
		t.Errorf("Projected = %+v", result.Projected)
	}
	if len(result.Mappings) != 1 {
		t.Errorf("Mappings = %+v", result.Mappings)
	}
}

// A projection that carries an excluded value back is an error, not a result.
func TestIntegrationProjectRefusesALeakedValue(t *testing.T) {
	testfixtures.WithScriptedProvider(t, fmt.Sprintf(
		`{"projected":{"user_id":"u1","display_name":"Ada (%s)"},"confidence":0.9}`, testHash), nil)

	result, err := schemaflux.Project[internalUser, publicProfile](
		sampleUser(), schemaflux.ProjectOptions{Exclude: []string{"password_hash"}})

	if err == nil {
		t.Fatalf("a projection carrying an excluded value must be an error; got %+v", result.Projected)
	}
	if result.Projected.UserID != "" {
		t.Error("a refused projection must not return the payload it refused")
	}
}

// A provider failure is still an error rather than an empty profile.
func TestIntegrationProjectPropagatesProviderErrors(t *testing.T) {
	testfixtures.WithScriptedProvider(t, "I'm sorry, I can't help with that.", nil)

	if _, err := schemaflux.Project[internalUser, publicProfile](
		sampleUser(), schemaflux.ProjectOptions{Exclude: []string{"ssn"}}); err == nil {
		t.Fatal("an unparseable body must be an error")
	}
}

// Example_projectWithholdsFields shows what Exclude guarantees: the named
// fields are removed from the payload before it is serialised, so their values
// never reach the provider. It runs under go test with a scripted provider: no
// credential, no spend.
func Example_projectWithholdsFields() {
	provider := testfixtures.NewScripted(`{"projected":{"user_id":"u1","display_name":"Ada Lovelace"},"confidence":0.93}`)
	schemaflux.NewClient("example-key").WithProviderInstance(provider)

	result, err := schemaflux.Project[internalUser, publicProfile](
		sampleUser(),
		schemaflux.ProjectOptions{
			Mappings: map[string]string{"id": "user_id"},
			Exclude:  []string{"ssn", "password_hash"},
			Steering: "Combine first_name and last_name into display_name",
		})
	if err != nil {
		fmt.Println("projection failed:", err)
		return
	}

	fmt.Println("profile:", result.Projected.DisplayName)

	sent := renderRequest(provider.Requests()[0])
	fmt.Println("request contains the SSN:", strings.Contains(sent, testSSN))
	fmt.Println("request contains the email:", strings.Contains(sent, "ada@example.com"))

	// Output:
	// profile: Ada Lovelace
	// request contains the SSN: false
	// request contains the email: true
}
