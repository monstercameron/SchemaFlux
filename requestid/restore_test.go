package requestid_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/requesttracking"
	"github.com/monstercameron/schemaflux/v2/internal/setting/settingtest"
	"github.com/monstercameron/schemaflux/v2/requestid"
)

func TestConfigureFollowsTheConventionOfEveryRestore(t *testing.T) {
	const prefix = "X-Setting-"
	settingtest.Convention(t, settingtest.Subject{
		Name: "requestid.Configure",
		Set: func(n int) func() {
			restore, err := requestid.Configure(requestid.Config{Header: prefix + strconv.Itoa(n)})
			if err != nil {
				t.Errorf("Configure: %v", err)
				return func() {}
			}
			return restore
		},
		Current: func() int {
			header := requesttracking.GetConfig().RequestIDHeader
			if !strings.HasPrefix(header, prefix) {
				if header != "X-Request-ID" {
					t.Errorf("the request header in force is %q", header)
				}
				return 0
			}
			n, err := strconv.Atoi(strings.TrimPrefix(header, prefix))
			if err != nil {
				t.Errorf("the request header in force is %q", header)
			}
			return n
		},
	})
}

// TestThereIsOneConstantForEachWayOfMakingACorrelationId reads the package's
// source and not its behaviour, because a second name for a way that already has
// one cannot be told from it by behaviour: using the request id as the
// correlation id is what CorrelationInherit does when the context carries none,
// and it is the zero value, so there is no constant of its own for it.
func TestThereIsOneConstantForEachWayOfMakingACorrelationId(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "config.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			for _, name := range spec.(*ast.ValueSpec).Names {
				if strings.HasPrefix(name.Name, "Correlation") {
					names = append(names, name.Name)
				}
			}
		}
	}
	want := []string{"CorrelationInherit", "CorrelationGenerate", "CorrelationNone"}
	if fmt.Sprint(names) != fmt.Sprint(want) {
		t.Errorf("the Correlation constants are %v, want %v", names, want)
	}
	if requestid.CorrelationInherit != 0 {
		t.Errorf("CorrelationInherit is %d, and it is the zero value", requestid.CorrelationInherit)
	}
}

func TestEveryValueOfAStrategyAndOfACorrelationIsAcceptedOrRefusedAndNeverPanics(t *testing.T) {
	const strategies, correlations = 5, 3
	for v := 0; v <= 255; v++ {
		for _, c := range []requestid.Config{
			{Strategy: requestid.Strategy(v)},
			{Correlation: requestid.Correlation(v)},
		} {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Errorf("Configure(%+v) panicked: %v", c, r)
					}
				}()
				before := requesttracking.GetConfig()
				restore, err := requestid.Configure(c)
				valid := int(c.Strategy) < strategies && int(c.Correlation) < correlations
				if valid {
					if err != nil || restore == nil {
						t.Errorf("Configure(%+v) = %v", c, err)
						return
					}
					restore()
					if after := requesttracking.GetConfig(); after != before {
						t.Errorf("after the restore of %+v the configuration is %+v, want %+v", c, after, before)
					}
					return
				}
				var e *schemaflux.Error
				if !errors.As(err, &e) || e.Kind != schemaflux.ErrConfiguration || restore != nil {
					t.Errorf("Configure(%+v) = %v, restore %v; want an *Error of kind configuration and no restore", c, err, restore != nil)
				}
				if after := requesttracking.GetConfig(); after != before {
					t.Errorf("a refused %+v changed the configuration to %+v", c, after)
				}
			}()
		}
	}
}
