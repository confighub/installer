// Copyright (C) ConfigHub, Inc.
// SPDX-License-Identifier: MIT

package upload

import (
	"fmt"
	"io"
	"sort"
	"strings"

	goclientnew "github.com/confighub/sdk/core/openapi/goclient-new"
)

// Emptied lists the Units a result empties, as <space>/<unit>.
func Emptied(results []*goclientnew.UploadResult) []string {
	var out []string
	for _, r := range results {
		for _, c := range r.Components {
			for _, s := range c.Spaces {
				for _, u := range s.Units {
					if u.Action == "Empty" {
						out = append(out, s.SpaceSlug+"/"+u.Slug)
					}
				}
			}
		}
	}
	return out
}

// Failed reports whether any Unit or Link write in a result failed. The writes
// that succeeded are real either way.
func Failed(r *goclientnew.UploadResult) bool {
	for _, c := range r.Components {
		for _, s := range c.Spaces {
			for _, u := range s.Units {
				if u.Error != nil {
					return true
				}
			}
			for _, l := range s.Links {
				if l.Error != nil {
					return true
				}
			}
		}
	}
	return false
}

// Report prints what an upload did, or on a dry run what it would do. With
// showMutations, each Unit a dry run would change is followed by the paths it
// changes and any it withholds as conflicts.
func Report(w io.Writer, r *goclientnew.UploadResult, showMutations bool) {
	for _, c := range r.Components {
		for _, s := range c.Spaces {
			fmt.Fprintf(w, "Space %s (%s)\n", s.SpaceSlug, s.Action)
			unchanged := 0
			for _, u := range s.Units {
				if u.Error != nil {
					fmt.Fprintf(w, "  %-9s %s: %s\n", "FAILED", u.Slug, errString(u.Error))
					continue
				}
				if u.Action == "Unchanged" {
					unchanged++
					continue
				}
				fmt.Fprintf(w, "  %-9s %s\n", u.Action, u.Slug)
				if showMutations {
					reportMutations(w, u)
				}
			}
			if unchanged > 0 {
				fmt.Fprintf(w, "  %-9s %d Unit(s)\n", "Unchanged", unchanged)
			}
			for _, l := range s.Links {
				if l.Error != nil {
					fmt.Fprintf(w, "  link FAILED %s -> %s: %s\n", l.FromUnit, l.ToUnit, errString(l.Error))
					continue
				}
				if l.Action != "Unchanged" {
					fmt.Fprintf(w, "  %-9s link %s -> %s (%s)\n", l.Action, l.FromUnit, l.ToUnit, l.Reason)
				}
			}
		}
		for _, b := range c.BrokenLinks {
			fmt.Fprintf(w, "broke %s link %s -> %s to resolve cycle: %s\n",
				b.Kind, b.From, b.To, strings.Join(b.Cycle, " -> "))
		}
		if len(c.SkippedSecrets) > 0 {
			fmt.Fprintf(w, "\nNote: %d Secret(s) were NOT uploaded. Apply them out-of-band:\n", len(c.SkippedSecrets))
			for _, s := range c.SkippedSecrets {
				fmt.Fprintf(w, "  - %s\n", s)
			}
		}
		if len(c.UnmatchedReferences) > 0 {
			fmt.Fprintln(w, "\nNote: the following references didn't resolve to any uploaded Unit (expected when the")
			fmt.Fprintln(w, "target lives in the cluster, e.g. a Secret created out-of-band):")
			for _, u := range c.UnmatchedReferences {
				fmt.Fprintf(w, "  - %s -> %s %q\n", u.FromUnit, u.TargetType, u.TargetName)
			}
		}
	}
}

func reportMutations(w io.Writer, u goclientnew.UploadUnitResult) {
	if u.Mutations != nil {
		paths := make([]string, 0, len(*u.Mutations))
		for path := range *u.Mutations {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			fmt.Fprintf(w, "      ~ %s\n", path)
		}
	}
	if u.Conflicts != nil {
		for _, c := range *u.Conflicts {
			fmt.Fprintf(w, "      ! conflict at %s\n", c.Path)
		}
	}
}

func errString(e *goclientnew.ResponseError) string {
	if e == nil || e.Message == "" {
		return "unknown error"
	}
	return e.Message
}

// Summary counts the Unit actions across the results of one upload or plan,
// every package's request together.
type Summary struct {
	Create, Update, Empty, Revive, Adopt int
}

// Summarize counts the Unit actions in results. A failed write is not counted.
func Summarize(results []*goclientnew.UploadResult) Summary {
	var s Summary
	for _, r := range results {
		for _, c := range r.Components {
			for _, sp := range c.Spaces {
				for _, u := range sp.Units {
					if u.Error != nil {
						continue
					}
					switch u.Action {
					case "Create":
						s.Create++
					case "Update":
						s.Update++
					case "Empty":
						s.Empty++
					case "Revive":
						s.Revive++
					case "Adopt":
						s.Adopt++
					}
				}
			}
		}
	}
	return s
}

// Changes reports whether anything would be, or was, written to a Unit.
func (s Summary) Changes() bool {
	return s.Create+s.Update+s.Empty+s.Revive+s.Adopt > 0
}

// Plan renders the counts a dry run would write, or "No changes."
func (s Summary) Plan() string {
	if !s.Changes() {
		return "No changes."
	}
	return fmt.Sprintf("Plan: %d to create, %d to update, %d to empty, %d to revive, %d to adopt.",
		s.Create, s.Update, s.Empty, s.Revive, s.Adopt)
}

// Applied renders the counts an upload wrote, or "No changes."
func (s Summary) Applied() string {
	if !s.Changes() {
		return "No changes."
	}
	return fmt.Sprintf("Applied: %d created, %d updated, %d emptied, %d revived, %d adopted.",
		s.Create, s.Update, s.Empty, s.Revive, s.Adopt)
}
