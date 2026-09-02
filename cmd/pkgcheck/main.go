package main

import (
	"fmt"
	"os"

	"github.com/MihaiPosea/cairn/internal/pkgs"
)

func main() {
	for _, root := range os.Args[1:] {
		g, err := pkgs.Load(root)
		if err != nil {
			fmt.Printf("%-40s ERROR %v\n", root, err)
			continue
		}
		d, _ := pkgs.Compare(root, g)
		line := fmt.Sprintf("%-38s %-22s declared=%-3d locked=%-4d",
			shorten(root), g.Source, g.DeclaredCount(), len(g.Packages))
		if d != nil {
			line += fmt.Sprintf(" installed=%-4d locked-only=%-4d disk-only=%d",
				d.InstalledCount, len(d.LockedNotInstalled), len(d.InstalledNotLocked))
		}
		fmt.Println(line)
	}
}

func shorten(s string) string {
	if len(s) > 36 {
		return "…" + s[len(s)-35:]
	}
	return s
}
