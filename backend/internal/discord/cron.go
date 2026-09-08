// Cron (5-field) parsing for bot schedules + the create flow used by the
// API handlers. Standard cron semantics: minute hour day-of-month month
// day-of-week, supporting *, lists, ranges and */n steps.
package discord

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ParseCron validates a 5-field cron expression and returns the interval to
// the next fire time from now (used for next_run_at seeding).
func ParseCron(expr string) (time.Duration, error) {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return 0, fmt.Errorf("cron must have 5 fields (minute hour day-of-month month day-of-week)")
	}
	now := time.Now().UTC().Truncate(time.Minute)
	next, err := nextCronFire(fields, now.Add(time.Minute))
	if err != nil {
		return 0, err
	}
	return next.Sub(now), nil
}

// NextCronAfter returns the interval to the next fire after at.
func NextCronAfter(expr string, at time.Time) time.Duration {
	fields := strings.Fields(strings.TrimSpace(expr))
	if len(fields) != 5 {
		return 0
	}
	next, err := nextCronFire(fields, at.Truncate(time.Minute).Add(time.Minute))
	if err != nil {
		return 0
	}
	return next.Sub(at)
}

// nextCronFire scans minute-by-minute (bounded: ~1 year of minutes) for the
// first matching time.
func nextCronFire(fields []string, from time.Time) (time.Time, error) {
	matchers := make([]func(int) bool, 5)
	for i := 0; i < 5; i++ {
		m, err := cronField(fields[i], cronBounds[i])
		if err != nil {
			return time.Time{}, err
		}
		matchers[i] = m
	}
	both := fields[2] != "*" && fields[4] != "*"
	limit := from.Add(366 * 24 * time.Hour)
	for t := from; t.Before(limit); t = t.Add(time.Minute) {
		if !matchers[0](t.Minute()) || !matchers[1](t.Hour()) ||
			!matchers[3](int(t.Month())) {
			continue
		}
		if both {
			// Standard cron: match when EITHER day field matches.
			if !domOK(matchers[2], t) && !dowOK(matchers[4], t) {
				continue
			}
		} else {
			if !matchers[2](t.Day()) || !dowOK(matchers[4], t) {
				continue
			}
		}
		return t, nil
	}
	return time.Time{}, fmt.Errorf("cron expression never fires within a year")
}

func domOK(m func(int) bool, t time.Time) bool { return m(t.Day()) }

func dowOK(m func(int) bool, t time.Time) bool { return m(int(t.Weekday())) || m(int(sundayAs7(t))) }

func sundayAs7(t time.Time) int {
	if t.Weekday() == 0 {
		return 7
	}
	return int(t.Weekday())
}

var cronBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}

// cronField builds the matcher for one cron field.
func cronField(field string, bounds [2]int) (func(int) bool, error) {
	var parts []func(int) bool
	for _, seg := range strings.Split(field, ",") {
		step := 1
		if i := strings.Index(seg, "/"); i >= 0 {
			v, err := strconv.Atoi(seg[i+1:])
			if err != nil || v <= 0 {
				return nil, fmt.Errorf("invalid cron step in %q", field)
			}
			step = v
			seg = seg[:i]
		}
		lo, hi := bounds[0], bounds[1]
		switch {
		case seg == "*":
			// full range with step
		case strings.Contains(seg, "-"):
			parts2 := strings.SplitN(seg, "-", 2)
			a, err1 := strconv.Atoi(parts2[0])
			b, err2 := strconv.Atoi(parts2[1])
			if err1 != nil || err2 != nil || a < lo || b > hi || a > b {
				return nil, fmt.Errorf("invalid cron range in %q", field)
			}
			lo, hi = a, b
		default:
			v, err := strconv.Atoi(seg)
			if err != nil || v < lo || v > hi {
				return nil, fmt.Errorf("invalid cron value in %q", field)
			}
			if step == 1 {
				vv := v
				parts = append(parts, func(x int) bool { return x == vv })
				continue
			}
			lo, hi = v, bounds[1]
		}
		loCopy, hiCopy, stepCopy := lo, hi, step
		parts = append(parts, func(x int) bool {
			return x >= loCopy && x <= hiCopy && (x-loCopy)%stepCopy == 0
		})
	}
	return func(x int) bool {
		for _, p := range parts {
			if p(x) {
				return true
			}
		}
		return false
	}, nil
}

// ---------------------------------------------------------------------------
// create flow (used by the API handler)
// ---------------------------------------------------------------------------

// CreateInput is the validated-on-arrival create request.
type CreateInput struct {
	Name           string
	Runtime        string
	RuntimeVersion string
	StartupFile    string
	StartupCommand string
	BuildCommand   string
	Env            map[string]string
	GitRepo        string
	GitBranch      string
	GitToken       string
	RestartPolicy  string
	MaxRestarts    int
}

// CreateBot validates + inserts the bot row (installing state, encrypted
// env/token). The install job is enqueued by the caller.
func CreateBot(ctx context.Context, store *Store, orgID, serverID, actorID uuid.UUID, in CreateInput) (*Bot, error) {
	env, runtimeVersion, startupFile, err := ValidateCreate(
		in.Name, in.Runtime, in.RuntimeVersion, in.StartupFile, in.StartupCommand,
		in.GitRepo, in.RestartPolicy, in.Env)
	if err != nil {
		return nil, validation(err)
	}
	envEnc, err := EncryptEnv(env)
	if err != nil {
		return nil, validation(err)
	}
	tokenEnc, err := EncryptGitToken(in.GitToken)
	if err != nil {
		return nil, err
	}
	if in.MaxRestarts <= 0 {
		in.MaxRestarts = 5
	}
	return store.Create(ctx, orgID, serverID, actorID, in.Name, in.Runtime, runtimeVersion,
		startupFile, in.StartupCommand, in.BuildCommand, in.RestartPolicy, in.MaxRestarts,
		envEnc, in.GitRepo, in.GitBranch, tokenEnc, nil)
}

// validationError marks user-fixable errors (API maps to 422).
type validationError struct{ err error }

func (e *validationError) Error() string { return e.err.Error() }

func validation(err error) error { return &validationError{err: err} }

// IsValidationError reports whether err is user-fixable (422, not 500).
func IsValidationError(err error) bool {
	_, ok := err.(*validationError)
	return ok
}

// EnvEncOf is a tiny helper so call sites can pass "no env yet" explicitly.
func EnvEncOf(_ any) string { return "" }
