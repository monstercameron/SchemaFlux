# 0003 — The per-call provider seam uses a context value

**Status:** accepted

## Context

Every `Client` constructor wrote a package-global provider, so building a second
client silently repointed the first one's operations at the second one's
provider (**IN-004**, **TI-002**). Two clients in one process is a library
embedded twice, a test running beside application code, or a tenant per client —
all three were broken.

Carrying a dependency on a `context.Context` is a widely disliked pattern in Go,
for good reasons: it is invisible in signatures and unchecked at compile time.

## Departure

`ops.WithProvider(ctx, p)` puts the provider on the context, and
`Client.Context(ctx)` hands a caller a context carrying that client's provider.

## Why

**Go has no type-parameterised methods.** `client.Extract[T](...)` cannot be
written, which is precisely why the provider became a global in the first place.
So the seam has to be per-call, and the options struct was not available either:
`internal/llm` imports `internal/types`, so a `Provider` field on `OpOptions` is
an import cycle, and the workaround — an `any` field type-asserted back — is the
untyped landmine `dead_options_test.go` exists to keep out.

The context was what remained. It has one strong property here: **it needed zero
changes at roughly sixty call sites**, because every operation already threads
its options' context through. A seam that requires touching every operation is a
seam that gets applied to some of them.

**What it costs:** it is invisible in the signature. A caller who forgets
`client.Context(ctx)` silently gets the package default, and nothing warns them.

## When this should be revisited

When **IN-004**'s full redesign lands — the client owning an immutable execution
snapshot — this becomes the compatibility shim rather than the mechanism, and
the context value should stop being the primary path.
