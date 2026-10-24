package output

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/whitechain-labs/whitechain-state-migration/staterecon/internal/models"
)

const (
	filenameFull     = "report_full.csv"
	filenameMismatch = "report_mismatch.csv"
	filenameSummary  = "report_summary.txt"
)

func NewReporter(dir, prefix string) (*Reporter, error) {
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create output directory %q: %w", dir, err)
	}

	full, err := newCSVFile(filepath.Join(dir, prefixedFilename(prefix, filenameFull)))
	if err != nil {
		return nil, err
	}

	mismatch, err := newCSVFile(filepath.Join(dir, prefixedFilename(prefix, filenameMismatch)))
	if err != nil {
		full.close()
		return nil, err
	}

	return &Reporter{
		full:        full,
		mismatch:    mismatch,
		summaryPath: filepath.Join(dir, prefixedFilename(prefix, filenameSummary)),
	}, nil
}

func prefixedFilename(prefix, filename string) string {
	if prefix == "" {
		return filename
	}
	return prefix + "_" + filename
}

func newCSVFile(path string) (*csvFile, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, fmt.Errorf("create file %q: %w", path, err)
	}
	w := csv.NewWriter(f)
	header := []string{"address", "status", "field", "l1_value", "l2_value", "error"}
	if err := w.Write(header); err != nil {
		f.Close()
		return nil, fmt.Errorf("write csv header to %q: %w", path, err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		f.Close()
		return nil, fmt.Errorf("flush csv header to %q: %w", path, err)
	}
	return &csvFile{file: f, writer: w}, nil
}

func (r *Reporter) WriteRows(rows []models.ReportRow) error {
	for _, row := range rows {
		record := []string{
			row.Address,
			string(row.Status),
			row.Field,
			row.L1Value,
			row.L2Value,
			row.Error,
		}
		if err := r.full.writer.Write(record); err != nil {
			return fmt.Errorf("write full csv row %s/%s: %w", row.Address, row.Field, err)
		}
		if row.Status == models.StatusMismatch || row.Status == models.StatusError {
			if err := r.mismatch.writer.Write(record); err != nil {
				return fmt.Errorf("write mismatch csv row %s/%s: %w", row.Address, row.Field, err)
			}
		}
	}
	r.full.writer.Flush()
	if err := r.full.writer.Error(); err != nil {
		return err
	}
	r.mismatch.writer.Flush()
	return r.mismatch.writer.Error()
}

func (r *Reporter) WriteSummary(s *ReportSummary) error {
	f, err := os.Create(r.summaryPath)
	if err != nil {
		return fmt.Errorf("create summary file: %w", err)
	}
	defer f.Close()

	ts := s.GeneratedAt
	if ts.IsZero() {
		ts = time.Now()
	}

	sections := []string{
		"=== State Reconciliation Summary ===",
		fmt.Sprintf("Generated:             %s", ts.Format(time.RFC3339)),
		"",
		"--- Configuration ---",
		fmt.Sprintf("L1 RPC:                %s", s.L1RPC),
		fmt.Sprintf("L2 RPC:                %s", s.L2RPC),
		fmt.Sprintf("L1 Block:              %s", s.L1Block),
		fmt.Sprintf("L2 Block:              %s", s.L2Block),
		"",
		"--- Results ---",
		fmt.Sprintf("Pages:                 %d", s.Pages),
		fmt.Sprintf("Batches:               %d", s.Batches),
		fmt.Sprintf("Accounts total:        %d", s.Accounts),
		fmt.Sprintf("  Matched:             %d", s.Matched),
		fmt.Sprintf("  Mismatched:          %d", s.Mismatched),
		fmt.Sprintf("  Errors:              %d", s.Errors),
		"",
		"--- Mismatches by field ---",
		fmt.Sprintf("Balance:               %d", s.BalanceMismatches),
		fmt.Sprintf("Nonce:                 %d", s.NonceMismatches),
		fmt.Sprintf("Code hash:             %d", s.CodeHashMismatches),
		fmt.Sprintf("Storage hash:          %d", s.StorageHashMismatches),
		"",
		"--- Contract checks ---",
		fmt.Sprintf("Proof checks:          %d", s.ProofChecks),
		fmt.Sprintf("Contracts checked:     %d", s.ProofCheckAccounts),
		"",
	}

	for _, line := range sections {
		if _, err := fmt.Fprintln(f, line); err != nil {
			return fmt.Errorf("write summary: %w", err)
		}
	}
	return nil
}

func (r *Reporter) Close() error {
	err1 := r.full.close()
	err2 := r.mismatch.close()
	if err1 != nil {
		return err1
	}
	return err2
}

func (c *csvFile) close() error {
	if c == nil {
		return nil
	}
	c.writer.Flush()
	writerErr := c.writer.Error()
	closeErr := c.file.Close()
	if writerErr != nil {
		return writerErr
	}
	return closeErr
}
