package antispampolicy_test

import (
	"testing"

	"github.com/nicrepository/nchat/libs/go/platform/antispampolicy"
)

func TestValid(t *testing.T) {
	cases := []struct {
		name  string
		value int
		want  bool
	}{
		{"zero is not a policy", 0, false},
		{"negative is not a policy", -1, false},
		{"minimum", antispampolicy.Min, true},
		{"default", antispampolicy.Default, true},
		{"maximum", antispampolicy.Max, true},
		{"one past the maximum", antispampolicy.Max + 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := antispampolicy.Valid(tc.value); got != tc.want {
				t.Fatalf("Valid(%d) = %v, want %v", tc.value, got, tc.want)
			}
		})
	}
}

// The point of Effective is that no unreadable value ever becomes "no limit".
func TestEffective_NeverUnlimited(t *testing.T) {
	for _, value := range []int{0, -5, antispampolicy.Max + 1} {
		if got := antispampolicy.Effective(value); got != antispampolicy.Default {
			t.Fatalf("Effective(%d) = %d, want the default %d", value, got, antispampolicy.Default)
		}
	}
	if got := antispampolicy.Effective(30); got != 30 {
		t.Fatalf("Effective(30) = %d, want 30", got)
	}
}

// Zero must not be expressible: an anti-spam control that can be set to zero is
// a workspace mute wearing a different name.
func TestZeroIsNotExpressible(t *testing.T) {
	if antispampolicy.Min <= 0 {
		t.Fatalf("Min must be positive, got %d", antispampolicy.Min)
	}
}
