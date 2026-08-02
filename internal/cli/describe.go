package cli

import (
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bkum/weftly/internal/schema"
	"github.com/spf13/cobra"
)

// newDescribeCmd prints a workflow's input contract and presets. It is
// the CLI counterpart to what the SPA form shows, and the thing a person
// runs before writing `--input` flags — without it the only way to learn
// an input's type or allowed values is to open the YAML.
func newDescribeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "describe <workflow.yml>",
		Short: "Print a workflow's inputs, constraints, and presets",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			wf, err := schema.Load(args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "%s\n", wf.Name)
			if wf.Description != "" {
				fmt.Fprintf(out, "%s\n", strings.TrimSpace(wf.Description))
			}
			if wf.Library {
				fmt.Fprintf(out, "\n(library fragment — include-only, not directly runnable)\n")
			}

			if len(wf.Inputs) > 0 {
				fmt.Fprintf(out, "\nINPUTS\n")
				tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "  NAME\tTYPE\tREQUIRED\tDEFAULT\tCONSTRAINTS")
				names := make([]string, 0, len(wf.Inputs))
				for n := range wf.Inputs {
					names = append(names, n)
				}
				sort.Strings(names)
				for _, n := range names {
					in := wf.Inputs[n]
					req := ""
					if in.Required {
						req = "yes"
					}
					def := ""
					if in.HasDefault && in.Default != nil {
						def = fmt.Sprintf("%v", in.Default)
					}
					// Never print a secret's default, even if the author
					// wrote one — `weftly describe` output gets pasted
					// into tickets and chat.
					if in.Secret {
						def = "(secret)"
					}
					fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n", n, in.EffectiveType(), req, def, constraintSummary(in))
				}
				tw.Flush()
				// Descriptions are free text and don't tabulate well.
				for _, n := range names {
					if d := strings.TrimSpace(wf.Inputs[n].Description); d != "" {
						fmt.Fprintf(out, "\n  %s: %s\n", n, collapse(d))
					}
				}
			}

			if len(wf.Presets) > 0 {
				fmt.Fprintf(out, "\nPRESETS\n")
				pnames := make([]string, 0, len(wf.Presets))
				for n := range wf.Presets {
					pnames = append(pnames, n)
				}
				sort.Strings(pnames)
				for _, pn := range pnames {
					p := wf.Presets[pn]
					fmt.Fprintf(out, "  %s", pn)
					if p.Description != "" {
						fmt.Fprintf(out, " — %s", collapse(p.Description))
					}
					fmt.Fprintln(out)
					keys := make([]string, 0, len(p.Values))
					for k := range p.Values {
						keys = append(keys, k)
					}
					sort.Strings(keys)
					for _, k := range keys {
						fmt.Fprintf(out, "      %s: %v\n", k, p.Values[k])
					}
				}
				fmt.Fprintf(out, "\n  apply with: weftly run %s --preset <name>\n", args[0])
			}
			return nil
		},
	}
}

// constraintSummary renders an input's constraints in one short cell.
func constraintSummary(in schema.Input) string {
	var parts []string
	if vals := in.AllowedValues(); len(vals) > 0 {
		parts = append(parts, "one of: "+strings.Join(vals, "|"))
	}
	if in.Items != "" {
		parts = append(parts, "items="+string(in.Items))
	}
	if in.Min != nil {
		parts = append(parts, "min="+boundString(in, *in.Min))
	}
	if in.Max != nil {
		parts = append(parts, "max="+boundString(in, *in.Max))
	}
	if in.MinLen != nil {
		parts = append(parts, fmt.Sprintf("min_length=%d", *in.MinLen))
	}
	if in.MaxLen != nil {
		parts = append(parts, fmt.Sprintf("max_length=%d", *in.MaxLen))
	}
	if in.MinItems != nil {
		parts = append(parts, fmt.Sprintf("min_items=%d", *in.MinItems))
	}
	if in.MaxItems != nil {
		parts = append(parts, fmt.Sprintf("max_items=%d", *in.MaxItems))
	}
	if in.Pattern != "" {
		parts = append(parts, "pattern="+in.Pattern)
	}
	if in.MustExist {
		parts = append(parts, "must_exist")
	}
	if in.Secret {
		parts = append(parts, "secret")
	}
	return strings.Join(parts, ", ")
}

// boundString renders a numeric bound in the units its type implies, so
// a duration max reads `10m` rather than `600000000000`.
func boundString(in schema.Input, f float64) string {
	if in.EffectiveType() == schema.InputDuration {
		return schema.EnvString(durationFromFloat(f))
	}
	return schema.EnvString(f)
}

func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// durationFromFloat converts a nanosecond bound back into a Duration.
func durationFromFloat(f float64) time.Duration { return time.Duration(int64(f)) }
