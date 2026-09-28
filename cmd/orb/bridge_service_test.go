package main

import (
	"bufio"
	"strings"
	"testing"
)

func TestBridgeUnitRestartsCrashesAndCarriesPath(t *testing.T) {
	unit := bridgeUnit("/home/o/.local/bin/orb", "personal", map[string]string{"PATH": "/home/o/.local/bin:/usr/bin", "ORB_BRIDGE_HOME": ""})
	for _, want := range []string{`ExecStart="/home/o/.local/bin/orb" bridge run --profile "personal"`, "Restart=on-failure", `Environment="PATH=/home/o/.local/bin:/usr/bin"`, "WantedBy=default.target"} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "ORB_BRIDGE_HOME") || bridgeUnitName("work") != "orb-bridge-work.service" {
		t.Fatalf("unit:\n%s", unit)
	}
}

func TestBridgeUnitKeepsPercentAndDollarLiteral(t *testing.T) {
	unit := bridgeUnit("/home/o/100%/$HOME/orb", "personal", map[string]string{"PATH": "/opt/50%:/usr/bin"})
	for _, want := range []string{`ExecStart="/home/o/100%%/$$HOME/orb"`, `Environment="PATH=/opt/50%%:/usr/bin"`} {
		if !strings.Contains(unit, want) {
			t.Fatalf("unit lacks %q:\n%s", want, unit)
		}
	}
}

func TestYesTakesTheDefaultOnlyForAnEmptyAnswer(t *testing.T) {
	for input, want := range map[string]bool{"\n": true, "y\n": true, "n\n": false, "": false, "later\n": false} {
		if got := yes(bufio.NewReader(strings.NewReader(input)), true); got != want {
			t.Errorf("%q: %v", input, got)
		}
	}
}
