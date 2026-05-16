package upload

import "strings"

// builtInClusterRoles is the four user-facing default ClusterRoles
// Kubernetes ships in every cluster
// (https://kubernetes.io/docs/reference/access-authn-authz/rbac/).
// They're not represented as Units in any ConfigHub Space because they
// pre-exist; references to them from package-provided RoleBindings /
// ClusterRoleBindings would otherwise show up as unmatched in the link
// inference reminder.
var builtInClusterRoles = map[string]struct{}{
	"cluster-admin": {},
	"admin":         {},
	"edit":          {},
	"view":          {},
}

// IsBuiltInClusterRole reports whether name is a Kubernetes built-in
// ClusterRole — either one of the four user-facing roles (cluster-admin,
// admin, edit, view) or any role under the `system:` prefix Kubernetes
// reserves for its core-component roles (system:node, system:kube-*,
// system:controller:*, etc.). See
// https://kubernetes.io/docs/reference/access-authn-authz/rbac/.
//
// Duplicated here pending the next SDK release; once a canonical copy
// lives in k8skit, drop this file and import from there.
func IsBuiltInClusterRole(name string) bool {
	if _, ok := builtInClusterRoles[name]; ok {
		return true
	}
	return strings.HasPrefix(name, "system:")
}
