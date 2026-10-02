package body

import "testing"

// TestSelectCoreIPv6 covers Core Doll Network IPv6 selection from persisted
// CoreAddresses. M2 persists CoreAddresses in potentially mixed form: bare IPv6
// ("fd00::2") and prefixed ("fd00::2/128"). The selector must accept both,
// ignore IPv4/other values, and fail closed (return "") when nothing valid.
func TestSelectCoreIPv6(t *testing.T) {
	tests := []struct {
		name string
		// build returns the membership under test (defaults to an active one).
		build    func() *Membership
		want     string
		wantFrom string // the element index intuition, informational
	}{
		{
			name: "bare IPv6 is selected",
			build: func() *Membership {
				return &Membership{Status: MembershipActive, CoreAddresses: []string{"fd00::2"}}
			},
			want: "fd00::2",
		},
		{
			name: "prefixed IPv6 is selected (normalized to bare address)",
			build: func() *Membership {
				return &Membership{Status: MembershipActive, CoreAddresses: []string{"fd00::2/128"}}
			},
			want: "fd00::2",
		},
		{
			name: "mixed bare, prefixed, and v4 picks first valid IPv6",
			build: func() *Membership {
				return &Membership{
					Status:        MembershipActive,
					CoreAddresses: []string{"192.0.2.10", "fd00::3", "fd00::4/128"},
				}
			},
			want: "fd00::3",
		},
		{
			name: "IPv4-only addresses yield nothing",
			build: func() *Membership {
				return &Membership{Status: MembershipActive, CoreAddresses: []string{"192.0.2.10", "198.51.100.7"}}
			},
			want: "",
		},
		{
			name: "empty CoreAddresses yields nothing",
			build: func() *Membership {
				return &Membership{Status: MembershipActive}
			},
			want: "",
		},
		{
			name: "empty strings are skipped",
			build: func() *Membership {
				return &Membership{Status: MembershipActive, CoreAddresses: []string{"", "", "fd00::9/64"}}
			},
			want: "fd00::9",
		},
		{
			name: "formatted (compressed) IPv6 is normalized",
			build: func() *Membership {
				// fd00:0:0:0:0:0:0:2 == fd00::2
				return &Membership{Status: MembershipActive, CoreAddresses: []string{"fd00:0:0:0:0:0:0:2"}}
			},
			want: "fd00::2",
		},
		{
			name: "nil membership yields nothing",
			build: func() *Membership {
				return nil
			},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.build().SelectCoreIPv6()
			if got != tt.want {
				t.Errorf("SelectCoreIPv6() = %q, want %q (informational: %s)", got, tt.want, tt.wantFrom)
			}
		})
	}
}

// TestSelectCoreIPv6AfterUnmarshal proves the selectable address survives a
// round-trip through the persisted membership envelope (the real shape the
// connect path reads from disk).
func TestSelectCoreIPv6AfterUnmarshal(t *testing.T) {
	m := &Membership{
		Status:        MembershipActive,
		NetworkID:     "net-1",
		CoreAddresses: []string{"fdab::9/128"},
	}
	raw, err := MarshalMembership(m)
	if err != nil {
		t.Fatalf("MarshalMembership: %v", err)
	}
	back, err := UnmarshalMembership(raw)
	if err != nil {
		t.Fatalf("UnmarshalMembership: %v", err)
	}
	if got := back.SelectCoreIPv6(); got != "fdab::9" {
		t.Errorf("SelectCoreIPv6() after round-trip = %q, want %q", got, "fdab::9")
	}
}
