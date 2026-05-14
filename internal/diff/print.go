package diff

import (
	"fmt"
	"io"
	"strings"
)

// Print renders a Plan in the terraform-style format documented in
// docs/lifecycle.md. Writes to w.
//
// Empty plan emits a single line ("No changes."). Plans with content
// emit a totals header, then per-Space sections, then a per-Space
// Images: footer.
func Print(w io.Writer, p Plan) {
	if !p.HasChanges() {
		// Still print the Images footer per Space — operators want to
		// see the eventual image set even when nothing else changed.
		fmt.Fprintln(w, "No changes.")
		printImageFooters(w, p)
		return
	}
	adds, updates, deletes := p.Counts()
	fmt.Fprintf(w, "Plan: %d to add, %d to change, %d to delete.\n", adds, updates, deletes)

	for _, sp := range p.Spaces {
		if len(sp.Adds) == 0 && len(sp.Updates) == 0 && len(sp.Deletes) == 0 {
			continue
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Space %s:\n", sp.SpaceSlug)
		for _, a := range sp.Adds {
			fmt.Fprintf(w, "  + %s\n", a.Slug)
		}
		for _, u := range sp.Updates {
			fmt.Fprintf(w, "  ~ %s\n", u.Slug)
			printIndentedBlock(w, u.DiffText, "      ")
		}
		for _, d := range sp.Deletes {
			fmt.Fprintf(w, "  - %s\n", d.Slug)
		}
	}
	printImageFooters(w, p)
}

func printImageFooters(w io.Writer, p Plan) {
	for _, sp := range p.Spaces {
		if len(sp.Images) == 0 {
			continue
		}
		fmt.Fprintln(w)
		fmt.Fprintf(w, "Images in %s (post-render):\n", sp.SpaceSlug)
		for _, img := range sp.Images {
			tag := "    "
			if img.Init {
				tag = "  init"
			}
			fmt.Fprintf(w, "%s  %s/%s [%s] %s\n", tag, img.Kind, img.Name, img.Container, img.Image)
		}
	}
}

// printIndentedBlock writes block to w, prefixing each non-empty line
// with prefix. Used to nest cub's mutations output under a "~ slug"
// header. Trailing blank lines are dropped.
func printIndentedBlock(w io.Writer, block, prefix string) {
	block = strings.TrimRight(block, "\n")
	if block == "" {
		return
	}
	for _, line := range strings.Split(block, "\n") {
		if line == "" {
			fmt.Fprintln(w)
			continue
		}
		fmt.Fprintf(w, "%s%s\n", prefix, line)
	}
}
