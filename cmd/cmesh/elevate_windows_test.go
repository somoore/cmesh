//go:build windows

package main

import "testing"

func TestWindowsCommandLineArg(t *testing.T) {
	cases := map[string]string{
		`_remove-service`:        `_remove-service`,
		`C:\Program Files\cmesh`: `"C:\Program Files\cmesh"`,
		`quoted"value`:           `"quoted\"value"`,
		`trailing\`:              `trailing\`,
		`trail space\`:           `"trail space\\"`,
	}
	for in, want := range cases {
		if got := windowsCommandLineArg(in); got != want {
			t.Fatalf("windowsCommandLineArg(%q) = %q, want %q", in, got, want)
		}
	}
}
