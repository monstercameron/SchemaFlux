package requesttracking

// PinDefaults makes the default configuration the one in force, whatever the
// environment says, until Configure is called: tracking on, the request id made
// by the "auto" strategy, the correlation id inherited, and the headers
// X-Request-ID and X-Correlation-ID. The five SCHEMAFLUX_REQUEST_* variables are
// not read, so a value that cannot be used is not reported either.
//
// It is for a program that takes none of its configuration from the
// environment. It does nothing when a configuration is already in force, a
// Configure's or an earlier PinDefaults', so it can be called from every
// package that wants the defaults without one undoing what another configured.
func PinDefaults() {
	configMu.Lock()
	defer configMu.Unlock()
	if configured {
		return
	}
	configuredCfg = normalizeConfig(Config{
		Enabled:               true,
		RequestIDStrategy:     IDStrategyAuto,
		CorrelationIDStrategy: CorrelationStrategyInherit,
		RequestIDHeader:       "X-Request-ID",
		CorrelationIDHeader:   "X-Correlation-ID",
	})
	configured = true
}
