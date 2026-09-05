package main

import (
	"testing"
	"time"
)

func TestCron(t *testing.T) {
	c, err := parseCron("*/15 8-10 * * 1-5")
	if err != nil {
		t.Fatal(err)
	}
	if !c.matches(time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC)) {
		t.Fatal("expected weekday to match")
	}
	if c.matches(time.Date(2026, 8, 2, 9, 30, 0, 0, time.UTC)) {
		t.Fatal("expected Sunday not to match")
	}
}

func TestCronRejectsInvalid(t *testing.T) {
	for _, s := range []string{"* * * *", "61 * * * *", "*/0 * * * *"} {
		if _, err := parseCron(s); err == nil {
			t.Fatalf("expected %q to fail", s)
		}
	}
}
