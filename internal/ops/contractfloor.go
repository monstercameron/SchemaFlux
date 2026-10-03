package ops

import (
	"context"

	"github.com/monstercameron/schemaflux/internal/types"
)

// AttachedFloor returns the lowest contract level a policy attached to ctx with
// WithContractPolicy allows, and whether one was attached. It reads that policy
// and nothing else: not the client's, whose lists contractPolicyFrom copies. A
// call that has to know only whether it is under a floor asks this, and a
// context that carries no policy costs it one lookup and no allocation.
func AttachedFloor(ctx context.Context) (types.ContractLevel, bool) {
	if ctx == nil {
		return types.ContractPromptOnly, false
	}
	attached, ok := ctx.Value(contractPolicyKey{}).(types.DataPolicy)
	return attached.MinimumContract, ok
}
