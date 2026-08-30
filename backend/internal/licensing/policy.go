package licensing

import "time"

// Policy decides whether the panel may operate given its license state and
// connectivity history. Keeping evaluation pure makes it easy to unit test.
type Policy struct {
	GraceEnabled bool
	GracePeriod  time.Duration
}

type Decision struct {
	Usable  bool   // may the product continue operating?
	State   string // one of the Status* constants
}

// Evaluate derives the operational state.
//
//	req    - requested/required operation time basis (usually time.Now())
func (p Policy) Evaluate(
	hasLicense bool,
	status string,
	lastValidatedAt *time.Time,
	expiresAt *time.Time,
	now time.Time,
) Decision {
	if !hasLicense || status == "" || status == StatusInactive {
		return Decision{Usable: false, State: StatusInactive}
	}
	if expiresAt != nil && now.After(*expiresAt) {
		return Decision{Usable: false, State: StatusExpired}
	}
	switch status {
	case StatusSuspended:
		return Decision{Usable: false, State: StatusSuspended}
	case StatusInvalid:
		return Decision{Usable: false, State: StatusInvalid}
	}

	if p.GraceEnabled && lastValidatedAt != nil && p.GracePeriod > 0 {
		if since := now.Sub(*lastValidatedAt); since <= p.GracePeriod {
			state := status
			if status != StatusActive {
				state = StatusGrace
			}
			return Decision{Usable: true, State: state}
		}
		return Decision{Usable: false, State: StatusInvalid}
	}
	// Without grace: only explicitly active licenses are usable.
	return Decision{Usable: status == StatusActive, State: status}
}

// LastValidatedStale reports whether a background re-validation is due.
func (p Policy) LastValidatedStale(lastValidatedAt *time.Time, revalidateEvery time.Duration, now time.Time) bool {
	if lastValidatedAt == nil || revalidateEvery <= 0 {
		return false
	}
	return now.Sub(*lastValidatedAt) >= revalidateEvery
}
