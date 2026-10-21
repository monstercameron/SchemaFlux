package ops

// MaxRetries is the most times a request that failed in transit is sent again:
// a count above it, from a client or from a provider's own retry policy, is held
// to it. It is far above any number a caller means, and low enough that a count
// plus one, and the sum of a count and a backoff, cannot wrap.
const MaxRetries = 1 << 16
