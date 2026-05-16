package upload

import "testing"

func TestIsBuiltInClusterRole(t *testing.T) {
	for _, tc := range []struct {
		name string
		want bool
	}{
		// Four user-facing default roles from the Kubernetes RBAC docs.
		{"cluster-admin", true},
		{"admin", true},
		{"edit", true},
		{"view", true},

		// Core-component roles live under the `system:` prefix.
		{"system:node", true},
		{"system:kube-controller-manager", true},
		{"system:controller:deployment-controller", true},
		{"system:auth-delegator", true},

		// User-defined roles are not filtered.
		{"my-team-admin", false},
		{"app-reader", false},
		{"", false},

		// Looks like a system prefix but isn't (no colon) — not a built-in.
		{"systemic-role", false},

		// Case-sensitive: the canonical `system:` prefix is lowercase.
		{"System:node", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBuiltInClusterRole(tc.name); got != tc.want {
				t.Errorf("IsBuiltInClusterRole(%q) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}
