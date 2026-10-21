package config

import (
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The settings every call consults are read from the environment once, the first
// time they are needed, and kept. They were read again on every call: on Windows
// an os.Getenv is a system call that allocates its key and its buffer, and one
// Extract made fifteen of them (eight of those to find out that metrics were
// off), so a variable nobody had set cost the same as one somebody had.
//
// The environment is read again when ReloadEnvironment is called. A program that
// changes a SCHEMAFLUX_* variable after its first call and wants the change seen
// calls ReloadEnvironment; one that does not, keeps what the first call read.
// The setters (SetDebugMode, SetMetricsEnabled) are not part of this: they take
// effect at once and are read before the environment is.
//
// The variables are read one at a time, each when something first asks for it,
// so a malformed SCHEMAFLUX_TIMEOUT is reported by the first call that needs the
// timeout and not by a call that only asks whether metrics are on.

// environmentCell holds one setting read from the environment. A cell is empty
// until it is first read, and empty again after ReloadEnvironment.
//
// The read side is one atomic load. The first read takes the cell's own lock, so
// that goroutines arriving together find one value read by one of them and not
// one read each, and a read that reports a malformed value reports it once.
type environmentCell[T any] struct {
	mu    sync.Mutex
	value atomic.Pointer[T]
}

// environmentCells is every cell, so ReloadEnvironment can empty them without a
// list kept by hand beside them.
var (
	environmentCellsMu sync.Mutex
	environmentCells   []interface{ forget() }
)

func newEnvironmentCell[T any]() *environmentCell[T] {
	cell := &environmentCell[T]{}
	environmentCellsMu.Lock()
	environmentCells = append(environmentCells, cell)
	environmentCellsMu.Unlock()
	return cell
}

// environmentReads counts the reads that cells have made, so that a test can
// tell a setting that was kept from one that was read again on every call, on
// any platform: how much an os.Getenv costs differs between them.
var environmentReads atomic.Int64

// get returns the cell's value, reading it with read the first time.
func (c *environmentCell[T]) get(read func() T) T {
	if held := c.value.Load(); held != nil {
		return *held
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if held := c.value.Load(); held != nil {
		return *held
	}
	environmentReads.Add(1)
	value := read()
	c.value.Store(&value)
	return value
}

func (c *environmentCell[T]) forget() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.value.Store(nil)
}

// modelOverrides are the variables that name a model: SCHEMAFLUX_MODEL, which
// names one for every tier, and one for each tier.
type modelOverrides struct {
	all, smart, fast, quick string
}

var (
	debugEnvironment     = newEnvironmentCell[bool]()
	metricsEnvironment   = newEnvironmentCell[bool]()
	modelEnvironment     = newEnvironmentCell[modelOverrides]()
	repairEnvironment    = newEnvironmentCell[int]()
	retriesEnvironment   = newEnvironmentCell[int]()
	backoffEnvironment   = newEnvironmentCell[time.Duration]()
	timeoutEnvironment   = newEnvironmentCell[time.Duration]()
	reinforceEnvironment = newEnvironmentCell[bool]()
)

// forgetFunc is a function that forgets a setting kept outside this package.
type forgetFunc func()

func (f forgetFunc) forget() { f() }

// OnEnvironmentReload registers forget to be called by every ReloadEnvironment,
// after the settings of this package have been forgotten. It is for a package
// that keeps settings read from the environment of its own and cannot be
// imported from here (internal/requesttracking, which imports this package).
// Registrations last for the life of the process, so a package registers once,
// from its init.
func OnEnvironmentReload(forget func()) {
	environmentCellsMu.Lock()
	environmentCells = append(environmentCells, forgetFunc(forget))
	environmentCellsMu.Unlock()
}

// ReloadEnvironment forgets what has been read from the environment, so the
// next call that needs a setting reads it again. It is how a program that
// changes a SCHEMAFLUX_* variable while it runs makes the change count.
//
// The request-tracking settings are forgotten with the others, unless the
// program has given them in code (requesttracking.Configure), which the
// environment does not replace. A logger that has been built keeps the level it
// was built with.
func ReloadEnvironment() {
	environmentCellsMu.Lock()
	cells := append([]interface{ forget() }(nil), environmentCells...)
	environmentCellsMu.Unlock()
	for _, cell := range cells {
		cell.forget()
	}
}

// PromptReinforcementVariable turns the reinforcement block off. See
// PromptReinforcementEnabled.
const PromptReinforcementVariable = "SCHEMAFLUX_PROMPT_REINFORCEMENT"

// PromptReinforcementEnabled reports whether the reinforcement block is added
// to the system prompt. Anything but an explicit "0", "false", "off" or "no"
// leaves it on, because turning it off by accident changes behaviour silently.
func PromptReinforcementEnabled() bool {
	return reinforceEnvironment.get(func() bool {
		switch strings.ToLower(strings.TrimSpace(os.Getenv(PromptReinforcementVariable))) {
		case "0", "false", "off", "no":
			return false
		default:
			return true
		}
	})
}
