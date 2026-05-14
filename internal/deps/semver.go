package deps

import (
	"fmt"
	"sort"

	"github.com/Masterminds/semver/v3"
)

// parseConstraint parses a SemVer range expression. Empty input means "*"
// (any version).
func parseConstraint(expr string) (*semver.Constraints, error) {
	if expr == "" {
		expr = "*"
	}
	c, err := semver.NewConstraint(expr)
	if err != nil {
		return nil, fmt.Errorf("invalid version constraint %q: %w", expr, err)
	}
	return c, nil
}

// parseVersion is the same as semver.NewVersion but with a clearer error
// message. We strip a leading "v" because OCI tags conventionally use both
// "1.2.3" and "v1.2.3"; SemVer treats them equivalently.
func parseVersion(tag string) (*semver.Version, error) {
	return semver.NewVersion(tag)
}

// pickBest returns the highest tag from candidates whose SemVer parse
// satisfies constraint. Non-SemVer tags (channel aliases like "stable") are
// silently skipped. Returns ErrNoVersionMatches if nothing matches.
func pickBest(candidates []string, constraint *semver.Constraints) (string, *semver.Version, error) {
	type candidate struct {
		tag string
		ver *semver.Version
	}
	var matches []candidate
	for _, t := range candidates {
		v, err := parseVersion(t)
		if err != nil {
			continue
		}
		if !constraint.Check(v) {
			continue
		}
		matches = append(matches, candidate{tag: t, ver: v})
	}
	if len(matches) == 0 {
		return "", nil, ErrNoVersionMatches
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ver.LessThan(matches[j].ver)
	})
	best := matches[len(matches)-1]
	return best.tag, best.ver, nil
}

// ErrNoVersionMatches is returned by pickBest when no candidate tag's
// SemVer parse satisfies the constraint.
var ErrNoVersionMatches = fmt.Errorf("no version matches constraint")
