package output

import (
	"encoding/csv"
	"os"
	"time"
)

// ReportSummary holds all data written to report_summary.txt.
type ReportSummary struct {
	GeneratedAt           time.Time
	L1RPC                 string
	L2RPC                 string
	L1Block               string
	L2Block               string
	Pages                 int
	Batches               int
	Accounts              int
	Matched               int
	Mismatched            int
	Errors                int
	BalanceMismatches     int
	NonceMismatches       int
	CodeHashMismatches    int
	StorageHashMismatches int
	ProofChecks           int
	ProofCheckAccounts    int
}

type csvFile struct {
	file   *os.File
	writer *csv.Writer
}

// Reporter writes reconciliation results to three output files:
//   - report_full.csv     – every comparison row
//   - report_mismatch.csv – only mismatch and error rows
//   - report_summary.txt  – human-readable run summary
type Reporter struct {
	full        *csvFile
	mismatch    *csvFile
	summaryPath string
}
