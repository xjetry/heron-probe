package update

import "testing"

func TestValidVersion(t *testing.T) {
	for _, value := range []string{"v0.0.0", "v0.2.0", "v12.34.56", "v4294967295.4294967295.4294967295"} {
		if !ValidVersion(value) {
			t.Errorf("rejected stable version %q", value)
		}
	}
	for _, value := range []string{"", "0.2.0", "V0.2.0", "v01.2.0", "v1.02.0", "v1.2.00", "v1.2", "v1.2.3.4", "v1.2.3-rc1", "v1.2.3+build", "v1.2.3\n", "v+1.2.3", "v-1.2.3", "v1..3", "v4294967296.0.0", "v99999999999.0.0", "v１.2.3", "v1/2.3.4"} {
		if ValidVersion(value) {
			t.Errorf("accepted invalid version %q", value)
		}
	}
}

func TestNewer(t *testing.T) {
	for _, tc := range []struct {
		target, current string
		want            bool
	}{
		{"v1.0.0", "v0.99.99", true}, {"v1.2.0", "v1.1.99", true}, {"v1.2.10", "v1.2.9", true},
		{"v1.0.0", "v1.0.0", false}, {"v1.99.99", "v2.0.0", false}, {"v1.2.99", "v1.3.0", false}, {"v1.2.2", "v1.2.3", false},
		{"v1.2.3", "dev", false}, {"v1.2.3-rc1", "v1.2.2", false},
	} {
		if got := Newer(tc.target, tc.current); got != tc.want {
			t.Errorf("Newer(%q,%q)=%v want %v", tc.target, tc.current, got, tc.want)
		}
	}
}
