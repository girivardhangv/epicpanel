// Cron (5-field) parsing internals for Minecraft schedules.
package minecraft

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

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
			if !matchers[2](t.Day()) && !dowOK(matchers[4], t) {
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

// jsonUnmarshalProps decodes the properties JSON column.
func jsonUnmarshalProps(b []byte, out *map[string]string) error {
	return json.Unmarshal(b, out)
}
