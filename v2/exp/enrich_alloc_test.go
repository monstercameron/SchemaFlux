package exp

import (
	"context"
	"testing"

	schemaflux "github.com/monstercameron/schemaflux/v2"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/schemafluxtest"
)

type enrichAllocTarget struct {
	Name    string `json:"name"`
	Segment string `json:"segment"`
}

const enrichAllocReply = `{"enriched":{"name":"Ada","segment":"research"},"added_fields":["segment"],"confidence":{},"derivations":{}}`

func TestEnrichAllocations(t *testing.T) {
	model := &schemafluxtest.Provider{}
	model.Reply(enrichAllocReply)
	ctx := schemaflux.WithClient(context.Background(), schemaflux.Client{Provider: model})
	o := EnrichOptions{}
	c, err := enrichCommon(o, false)
	if err != nil {
		t.Fatal(err)
	}
	input := map[string]string{"name": "Ada"}
	a := testing.AllocsPerRun(20, func() { _, _ = Enrich[enrichAllocTarget](ctx, input) })
	b := testing.AllocsPerRun(20, func() { _, _ = ops.Enrich[map[string]string, enrichAllocTarget](input, enrichInternal(ctx, o, c)) })
	t.Logf("API %.2f internal %.2f", a, b)
}
