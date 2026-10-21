package schemaflux

import (
	"errors"
	"log/slog"

	"github.com/monstercameron/schemaflux/v2/internal/bind"
	"github.com/monstercameron/schemaflux/v2/internal/ops"
	"github.com/monstercameron/schemaflux/v2/internal/telemetry"
	"github.com/monstercameron/schemaflux/v2/internal/types"
)

// check returns the configuration error of the first setting of c that no call
// can use, naming the field, or nil. Nothing of it is read from the environment.
func (c Client) check() error {
	if c.Timeout < 0 {
		return types.Newf(types.KindConfiguration, "client", "Client.Timeout must not be negative")
	}
	if err := c.Budget.check(); err != nil {
		return err
	}
	if err := c.Scheduler.check(); err != nil {
		return err
	}
	if err := c.Policy.check(); err != nil {
		return err
	}
	if err := c.Recorder.check(); err != nil {
		var e *Error
		if errors.As(err, &e) {
			return types.Newf(types.KindConfiguration, "client", "%s", e.Message)
		}
		return err
	}
	return nil
}

// execConfig builds the snapshot a call on c runs on, with the environment read
// from env. It is built once for a WithClient or a SetDefault and never changed
// after, so every call on the client reads the same one.
//
// A client that cannot make a call has no Provider on its snapshot and says why
// in Err, which the call path returns before it sends anything. A client whose
// Provider is nil gets the one env names: built when this is called, or, for
// lazy, on the first request, which is what the default client does so that the
// environment is not read when the library is linked.
func (c Client) execConfig(env *environmentCell, lazy bool) *ops.ExecConfig {
	cfg := &ops.ExecConfig{
		Model:             c.Model,
		Effort:            types.Effort(c.Effort),
		Timeout:           c.Timeout,
		Retries:           min(c.Retries, ops.MaxRetries),
		Repairs:           min(c.Repairs, ops.MaxRetries),
		OmitReinforcement: c.OmitReinforcement,
		Logger:            quietLogger,
	}
	if cfg.Repairs == 0 {
		// The variable that used to set this for every client is gone.
		cfg.Repairs = ops.DefaultRepairAttempts
	}
	if c.Logger != nil {
		cfg.Logger = telemetry.FromSlog(slog.New(logGuard{c.Logger.Handler()}))
	}
	if err := c.check(); err != nil {
		cfg.Err = err
		return cfg
	}
	if c.Budget != nil {
		cfg.Budget = c.Budget.counter()
	}
	if c.Scheduler != nil {
		cfg.Scheduler = c.Scheduler.gate()
	}
	if c.Policy != nil {
		cfg.DataPolicy = c.Policy.internal()
		cfg.DataPolicyDeclared = true
	}
	if c.Recorder != nil {
		cfg.Sink = (*sink)(c.Recorder)
	}

	switch {
	case c.Provider != nil:
		cfg.Provider = bind.ToInternal(c.Provider)
	case lazy:
		cfg.Provider = &environmentProvider{checkTimeout: c.Timeout == 0}
	default:
		provider, err := env.resolve()
		if err != nil {
			cfg.Err = err
			return cfg
		}
		cfg.Provider = provider
	}

	if cfg.Timeout == 0 && !lazy {
		timeout, err := env.timeout()
		if err != nil {
			cfg.Provider, cfg.Err = nil, err
			return cfg
		}
		cfg.Timeout = timeout
		if cfg.Timeout == 0 {
			cfg.Timeout = defaultCallTimeout
		}
	}
	return cfg
}
