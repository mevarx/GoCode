package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/mevarx/GoCode/internal/config"
	"github.com/mevarx/GoCode/internal/session"
)

type sessionExportFlags struct {
	format  string
	output  string
	force   bool
	noTools bool
}

func newSessionCommand() *cobra.Command {
	sessionCmd := &cobra.Command{
		Use:   "session",
		Short: "Manage and export saved sessions",
	}

	var flags sessionExportFlags

	exportCmd := &cobra.Command{
		Use:   "export [session_id]",
		Short: "Export a stored session transcript to Markdown or JSON",
		Long: `Export a stored session transcript to Markdown or JSON.

If no session_id is provided, the most recently updated session is exported.
By default, the transcript is written to stdout in Markdown format.
System prompts and internal model reasoning content are always excluded.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadCLIConfig()
			if err != nil {
				return err
			}

			if !cfg.Session.Persist {
				return errors.New("session persistence is disabled in config")
			}

			dbPath := filepath.Join(config.SessionDir(), "sessions.db")
			store, err := session.NewStore(dbPath)
			if err != nil {
				return fmt.Errorf("failed to open session store: %w", err)
			}
			defer store.Close()

			var sessionID string
			if len(args) > 0 {
				sessionID = args[0]
			}

			return executeSessionExport(cmd.OutOrStdout(), store, true, sessionID, flags)
		},
	}

	exportCmd.Flags().StringVarP(&flags.format, "format", "f", "markdown", "Export format: markdown, json")
	exportCmd.Flags().StringVarP(&flags.output, "output", "o", "", "Output file path (default: stdout)")
	exportCmd.Flags().BoolVar(&flags.force, "force", false, "Allow overwriting an existing destination file")
	exportCmd.Flags().BoolVar(&flags.noTools, "no-tools", false, "Exclude tool invocations and execution results")

	sessionCmd.AddCommand(exportCmd)
	return sessionCmd
}

func executeSessionExport(out io.Writer, store *session.SessionStore, persistEnabled bool, sessionID string, flags sessionExportFlags) error {
	if !persistEnabled || store == nil {
		return errors.New("session persistence is disabled in config")
	}

	sessionID = strings.TrimSpace(sessionID)
	var rec *session.SessionRecord
	var err error

	if sessionID != "" {
		rec, err = store.GetSession(sessionID)
		if err != nil {
			return fmt.Errorf("failed to retrieve session %q: %w", sessionID, err)
		}
		if rec == nil {
			return fmt.Errorf("session %q not found", sessionID)
		}
	} else {
		rec, err = store.GetLastSession()
		if err != nil {
			return fmt.Errorf("failed to retrieve latest session: %w", err)
		}
		if rec == nil {
			return errors.New("no saved sessions found")
		}
	}

	var exportFmt session.ExportFormat
	switch strings.ToLower(strings.TrimSpace(flags.format)) {
	case "markdown", "md", "":
		exportFmt = session.FormatMarkdown
	case "json":
		exportFmt = session.FormatJSON
	default:
		return fmt.Errorf("unsupported export format: %q (supported: markdown, json)", flags.format)
	}

	opts := session.ExportOptions{
		Format:       exportFmt,
		IncludeTools: !flags.noTools,
	}

	data, err := session.Export(rec, opts)
	if err != nil {
		return fmt.Errorf("failed to export session: %w", err)
	}

	dest := strings.TrimSpace(flags.output)
	if dest == "" || dest == "-" {
		_, err = out.Write(data)
		return err
	}

	if err := writeExportFile(dest, data, flags.force); err != nil {
		return err
	}

	fmt.Fprintf(out, "Exported session %s to %s (%s)\n", rec.ID, dest, exportFmt)
	return nil
}

func writeExportFile(destPath string, data []byte, force bool) error {
	absPath, err := filepath.Abs(destPath)
	if err != nil {
		return fmt.Errorf("failed to resolve output path: %w", err)
	}

	dir := filepath.Dir(absPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create destination directory: %w", err)
	}

	fi, err := os.Lstat(absPath)
	if err == nil {
		if !force {
			return fmt.Errorf("output file %q already exists (use --force to overwrite)", destPath)
		}
		if fi.IsDir() {
			return fmt.Errorf("output path %q is a directory", destPath)
		}
		// If it's a symlink and force is requested, remove the link so we don't write through an arbitrary link target.
		if fi.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(absPath); err != nil {
				return fmt.Errorf("failed to replace symlink %q: %w", destPath, err)
			}
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to inspect output path: %w", err)
	}

	flags := os.O_CREATE | os.O_WRONLY
	if force {
		flags |= os.O_TRUNC
	} else {
		flags |= os.O_EXCL
	}

	f, err := os.OpenFile(absPath, flags, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}

	_, writeErr := f.Write(data)
	closeErr := f.Close()

	if writeErr != nil {
		_ = os.Remove(absPath)
		return fmt.Errorf("failed to write export content: %w", writeErr)
	}
	if closeErr != nil {
		_ = os.Remove(absPath)
		return fmt.Errorf("failed to close export file: %w", closeErr)
	}

	return nil
}
