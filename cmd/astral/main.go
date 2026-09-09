package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"astral/internal/query"
	"astral/internal/watcher"
)

func main() {
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	var rootCmd = &cobra.Command{
		Use:   "astral",
		Short: "a local, deterministic codebase advisor for LLMs",
		Long: `astral — a local, deterministic codebase advisor for LLMs.

It maintains a content-addressed index of symbols and human-authored
conventions notes, so an LLM can answer "where do I write this" and
"how do I write here" with a single cheap query.`,
	}

	var initCmd = &cobra.Command{
		Use:   "init [path]",
		Short: "build index + draft notes",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := root
			if len(args) > 0 {
				path = args[0]
			}
			app := query.New(path)
			idx, err := app.Indexer.Init()
			if err != nil {
				return err
			}
			fmt.Printf("indexed %d files in %d directories\n", len(idx.Files), len(idx.Dirs))
			return nil
		},
	}

	var watchCmd = &cobra.Command{
		Use:   "watch",
		Short: "warm-up watcher (lazy re-parse is default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := query.New(root)
			return watcher.Watch(app.Indexer, 500*time.Millisecond)
		},
	}

	var locateCmd = &cobra.Command{
		Use:   "locate <symbol>",
		Short: "file:line + scoped note (with state)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := query.New(root)
			out, err := app.Locate(args[0])
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	var moduleCmd = &cobra.Command{
		Use:   "module <path>",
		Short: "per-file summaries + module note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := query.New(root)
			out, err := app.Module(args[0])
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	var noteCmd = &cobra.Command{
		Use:   "note <module>",
		Short: "emit prompt / --set <json> / --approve",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := query.New(root)
			dir := args[0]
			set, _ := cmd.Flags().GetString("set")
			approve, _ := cmd.Flags().GetBool("approve")
			switch {
			case approve:
				if err := app.NoteApprove(dir); err != nil {
					return err
				}
				fmt.Printf("approved %s\n", dir)
			case set != "":
				if err := app.NoteSet(dir, set); err != nil {
					return err
				}
				fmt.Printf("stored draft note for %s\n", dir)
			default:
				out, err := app.NotePrompt(dir)
				if err != nil {
					return err
				}
				fmt.Print(out)
			}
			return nil
		},
	}
	noteCmd.Flags().String("set", "", "set note JSON as draft")
	noteCmd.Flags().Bool("approve", false, "approve note as reviewed")

	var reviewCmd = &cobra.Command{
		Use:   "review",
		Short: "batched: new, unclear, stale, conflict",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := query.New(root)
			out, err := app.Review()
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	var statusCmd = &cobra.Command{
		Use:   "status",
		Short: "freshness, coverage, staleness summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := query.New(root)
			out, err := app.Status()
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	var gcCmd = &cobra.Command{
		Use:   "gc",
		Short: "prune orphans",
		RunE: func(cmd *cobra.Command, args []string) error {
			app := query.New(root)
			if err := app.GC(); err != nil {
				return err
			}
			fmt.Println("pruned orphans")
			return nil
		},
	}

	var callersCmd = &cobra.Command{
		Use:   "callers <symbol>",
		Short: "files that reference a symbol across packages",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := query.New(root)
			out, err := app.Callers(args[0])
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	var affectedCmd = &cobra.Command{
		Use:   "affected <file...>",
		Short: "test files impacted by changes to the given files",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app := query.New(root)
			out, err := app.Affected(args)
			if err != nil {
				return err
			}
			fmt.Print(out)
			return nil
		},
	}

	rootCmd.AddCommand(initCmd, watchCmd, locateCmd, moduleCmd, noteCmd, reviewCmd, statusCmd, gcCmd, callersCmd, affectedCmd)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "astral:", err)
		os.Exit(1)
	}
}
