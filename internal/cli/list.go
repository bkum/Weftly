package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

func newListCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "Discover workflows in a directory (default ./workflows)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if dir == "" {
				dir = "./workflows"
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				return err
			}
			var files []string
			for _, e := range entries {
				if e.IsDir() {
					continue
				}
				name := e.Name()
				if strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml") {
					files = append(files, filepath.Join(dir, name))
				}
			}
			sort.Strings(files)
			for _, f := range files {
				fmt.Fprintln(cmd.OutOrStdout(), f)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dir, "dir", "", "directory to scan (default ./workflows)")
	return cmd
}
