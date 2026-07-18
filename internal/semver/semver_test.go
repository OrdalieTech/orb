package semver

import "testing"

func TestValid(t *testing.T) {
	valid := []string{"1.2.3", "v1.2.3", "0.0.0", "1.2.3-rc.1", "1.2.3-alpha.0.beta", "1.2.3+build", "1.2.3-rc.1+build.5"}
	for _, input := range valid {
		if !Valid(input) {
			t.Errorf("Valid(%q) = false, want true", input)
		}
	}
	invalid := []string{"", "1", "1.2", "1.2.x", "^1.2.3", "1.02.3", "1.2.3-", "a.b.c", ">=1.2.3", "1.2.3 - 2.0.0"}
	for _, input := range invalid {
		if Valid(input) {
			t.Errorf("Valid(%q) = true, want false", input)
		}
	}
}

func TestValidRange(t *testing.T) {
	valid := []string{"1.2.3", "^1.2.3", "~1.2", ">=1.2.3", "1.x", "*", "", "1.2.3 - 2.3.4", ">=1.0.0 <2.0.0", "^1.0.0 || ^2.0.0", "~>1.2.3", "=1.2.3"}
	for _, input := range valid {
		if !ValidRange(input) {
			t.Errorf("ValidRange(%q) = false, want true", input)
		}
	}
	invalid := []string{"not a version", "1.2.3garbage", "^^1.0.0"}
	for _, input := range invalid {
		if ValidRange(input) {
			t.Errorf("ValidRange(%q) = true, want false", input)
		}
	}
}

func TestSatisfies(t *testing.T) {
	cases := []struct {
		version, rng string
		want         bool
	}{
		{"1.2.3", "1.2.3", true},
		{"1.2.3", "=1.2.3", true},
		{"1.2.4", "1.2.3", false},
		{"1.4.0", "^1.2.3", true},
		{"2.0.0", "^1.2.3", false},
		{"1.2.2", "^1.2.3", false},
		{"0.2.5", "^0.2.3", true},
		{"0.3.0", "^0.2.3", false},
		{"0.0.3", "^0.0.3", true},
		{"0.0.4", "^0.0.3", false},
		{"1.2.9", "~1.2.3", true},
		{"1.3.0", "~1.2.3", false},
		{"1.9.9", "~1", true},
		{"2.0.0", "~1", false},
		{"1.5.0", "1.x", true},
		{"2.0.0", "1.x", false},
		{"1.2.5", "1.2.x", true},
		{"1.3.0", "1.2.x", false},
		{"5.0.0", "*", true},
		{"5.0.0", "", true},
		{"1.5.0", "1.2.3 - 2.3.4", true},
		{"2.3.4", "1.2.3 - 2.3.4", true},
		{"2.3.5", "1.2.3 - 2.3.4", false},
		{"2.3.9", "1.2.3 - 2.3", true},
		{"2.4.0", "1.2.3 - 2.3", false},
		{"1.5.0", ">=1.2.3 <2.0.0", true},
		{"2.0.0", ">=1.2.3 <2.0.0", false},
		{"3.0.0", "^1.0.0 || ^3.0.0", true},
		{"2.0.0", "^1.0.0 || ^3.0.0", false},
		{"2.0.0", ">1", true},
		{"1.5.0", ">1", false},
		{"1.5.0", ">1.2", true},
		{"1.2.9", ">1.2", false},
		{"0.9.0", "<1", true},
		{"1.0.0", "<1", false},
		{"1.9.0", "<=1", true},
		{"2.0.0", "<=1", false},
		// Prerelease inclusion rule.
		{"1.2.3-rc.1", "^1.2.3", false},
		{"1.2.3-rc.1", ">=1.2.3-rc.0", true},
		{"1.2.4-rc.1", ">=1.2.3-rc.0", false},
		{"1.2.3-rc.1", "1.2.3-rc.1", true},
		{"1.2.3-alpha.2", "~1.2.3-alpha.1", true},
	}
	for _, c := range cases {
		if got := Satisfies(c.version, c.rng); got != c.want {
			t.Errorf("Satisfies(%q, %q) = %v, want %v", c.version, c.rng, got, c.want)
		}
	}
}

func TestCompareOrdering(t *testing.T) {
	ordered := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "2.0.0", "2.1.0", "2.1.1"}
	for i := range len(ordered) - 1 {
		a, _ := Parse(ordered[i])
		b, _ := Parse(ordered[i+1])
		if Compare(a, b) >= 0 {
			t.Errorf("Compare(%q, %q) >= 0, want < 0", ordered[i], ordered[i+1])
		}
	}
}

func TestMaxSatisfying(t *testing.T) {
	versions := []string{"1.0.0", "1.2.0", "1.2.3", "1.3.0-rc.1", "2.0.0", "2.1.0"}
	cases := []struct {
		rng, want string
	}{
		{"^1.0.0", "1.2.3"},
		{"^2.0.0", "2.1.0"},
		{"~1.2.0", "1.2.3"},
		{"*", "2.1.0"},
		{"^3.0.0", ""},
		{">=1.3.0-rc.0 <1.3.0", "1.3.0-rc.1"},
	}
	for _, c := range cases {
		if got := MaxSatisfying(versions, c.rng); got != c.want {
			t.Errorf("MaxSatisfying(%q) = %q, want %q", c.rng, got, c.want)
		}
	}
}
