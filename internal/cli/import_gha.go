package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/bkum/weftly/internal/gha"
	"github.com/bkum/weftly/internal/schema"
	"github.com/spf13/cobra"
)

// newImportGHACmd wires `weftly import-gha` — reads a GitHub Actions
// workflow YAML from a file (or stdin with `-`), translates the
// supported subset into a weftly workflow, validates the result, and
// prints it to stdout or writes it to --out.
//
// Compile-time seam only. Nothing here is invoked at run time; the
// operator eyeballs the converted file and any warning notes before
// dropping it into a catalogue.
func newImportGHACmd() *cobra.Command {
	var (
		jobID   string
		outPath string
	)
	cmd := &cobra.Command{
		Use:   "import-gha <path-or-->",
		Short: "Convert a GitHub Actions workflow YAML to weftly YAML",
		Long: "Reads a GitHub Actions workflow file and prints the equivalent " +
			"weftly workflow to stdout (or --out). Translation notes for " +
			"unsupported constructs (uses:, matrix:, on:, ...) are printed to " +
			"stderr; the emitted YAML is always self-contained.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var reader io.Reader
			source := args[0]
			if source == "-" {
				reader = cmd.InOrStdin()
			} else {
				f, err := os.Open(source)
				if err != nil {
					return err
				}
				defer f.Close()
				reader = f
			}
			res, err := gha.Import(reader, gha.Options{JobID: jobID})
			if err != nil {
				return err
			}
			// Validate the translated YAML round-trips through the schema —
			// catches bugs where a translation change stops producing
			// something the engine would accept.
			wf, verr := schema.Parse(bytes.NewReader(res.YAML))
			if verr != nil {
				return fmt.Errorf("import-gha: translated YAML did not re-parse (please file a bug): %w", verr)
			}
			if verr := schema.Validate(wf); verr != nil {
				return fmt.Errorf("import-gha: translated YAML failed weftly validation: %w", verr)
			}

			for _, note := range res.Notes {
				fmt.Fprintln(cmd.ErrOrStderr(), "note: "+note)
			}
			banner := fmt.Sprintf("# converted from GitHub Actions (job=%s)\n", res.Job)
			out := append([]byte(banner), res.YAML...)
			if outPath != "" {
				return os.WriteFile(outPath, out, 0o644)
			}
			_, err = cmd.OutOrStdout().Write(out)
			return err
		},
	}
	cmd.Flags().StringVar(&jobID, "job", "", "pick a specific job id when the workflow has multiple")
	cmd.Flags().StringVarP(&outPath, "out", "o", "", "write the converted YAML to this file instead of stdout")
	return cmd
}
