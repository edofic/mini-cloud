package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type cronExpr struct{ fields [5]map[int]bool }

var cronBounds = [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 6}}

func parseCron(s string) (cronExpr, error) {
	var c cronExpr
	parts := strings.Fields(s)
	if len(parts) != 5 {
		return c, fmt.Errorf("expected five fields")
	}
	for i, p := range parts {
		set, err := parseCronField(p, cronBounds[i][0], cronBounds[i][1])
		if err != nil {
			return c, fmt.Errorf("field %d: %w", i+1, err)
		}
		c.fields[i] = set
	}
	return c, nil
}
func parseCronField(s string, min, max int) (map[int]bool, error) {
	out := map[int]bool{}
	for _, part := range strings.Split(s, ",") {
		step := 1
		base := part
		if x := strings.Split(part, "/"); len(x) == 2 {
			base = x[0]
			v, e := strconv.Atoi(x[1])
			if e != nil || v < 1 {
				return nil, fmt.Errorf("invalid step %q", part)
			}
			step = v
		} else if len(x) > 2 {
			return nil, fmt.Errorf("invalid %q", part)
		}
		lo, hi := min, max
		if base != "*" {
			if x := strings.Split(base, "-"); len(x) == 2 {
				var e error
				lo, e = strconv.Atoi(x[0])
				if e != nil {
					return nil, e
				}
				hi, e = strconv.Atoi(x[1])
				if e != nil {
					return nil, e
				}
			} else {
				v, e := strconv.Atoi(base)
				if e != nil {
					return nil, e
				}
				lo = v
				hi = v
			}
		}
		if lo < min || hi > max || lo > hi {
			return nil, fmt.Errorf("value outside %d-%d", min, max)
		}
		for v := lo; v <= hi; v += step {
			out[v] = true
		}
	}
	return out, nil
}
func (c cronExpr) matches(t time.Time) bool {
	return c.fields[0][t.Minute()] && c.fields[1][t.Hour()] && c.fields[2][t.Day()] && c.fields[3][int(t.Month())] && c.fields[4][int(t.Weekday())]
}
