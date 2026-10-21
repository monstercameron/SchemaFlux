package exp

import (
	"context"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type projectAllocationInput struct {
	Name string `json:"name"`
}
type projectAllocationOutput struct {
	Name string `json:"name"`
}

const projectAllocationAnswer = `{"projected":{"name":"A"},"mappings":[],"lost":[],"inferred":[],"confidence":0.8}`

func TestProjectAllocationsMatchTheInternalCall(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(projectAllocationAnswer)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	o := ProjectOptions{}
	c, err := projectCommon(o)
	if err != nil {
		t.Fatal(err)
	}
	input := projectAllocationInput{Name: "A"}
	api := testing.AllocsPerRun(20, func() { _, _ = Project[projectAllocationInput, projectAllocationOutput](ctx, input) })
	internal := testing.AllocsPerRun(20, func() {
		_, _ = ops.Project[projectAllocationInput, projectAllocationOutput](input, projectInternal(ctx, o, c))
	})
	t.Logf("allocations with client: API %.2f, internal %.2f", api, internal)
	if api != internal+1 {
		t.Errorf("with client: API %.2f allocations, internal %.2f; want one public result conversion", api, internal)
	}

	defaultModel := &schemafluxtest.Provider{}
	defaultModel.Reply(projectAllocationAnswer)
	schemafluxtest.Install(t, defaultModel)
	pinned, _ := ops.PinExec(context.Background())
	apiDefault := testing.AllocsPerRun(20, func() { _, _ = Project[projectAllocationInput, projectAllocationOutput](context.Background(), input) })
	internalDefault := testing.AllocsPerRun(20, func() {
		_, _ = ops.Project[projectAllocationInput, projectAllocationOutput](input, projectInternal(pinned, o, c))
	})
	t.Logf("allocations on default client: API %.2f, internal with pinned context %.2f", apiDefault, internalDefault)
	if apiDefault != internalDefault+2 {
		t.Errorf("default client: API %.2f allocations, internal %.2f; want conversion and default-client lookup", apiDefault, internalDefault)
	}
}
