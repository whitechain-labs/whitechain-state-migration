package models

import "strings"

type AccountType string

const (
	AccountTypeEOA      AccountType = "eoa"
	AccountTypeContract AccountType = "contract"
)

type Status string

const (
	StatusMatch    Status = "match"
	StatusMismatch Status = "mismatch"
	StatusError    Status = "error"
)

type AccountSnapshot struct {
	Address  string
	Balance  string
	Nonce    string
	CodeHash string
	Code     *string
	Type     AccountType
}

type ReportRow struct {
	Address string
	Status  Status
	Field   string
	L1Value string
	L2Value string
	Error   string
}

func JoinErrors(first, second string) string {
	switch {
	case first == "" && second == "":
		return ""
	case first == "":
		return strings.TrimSpace(second)
	case second == "":
		return strings.TrimSpace(first)
	default:
		return strings.TrimSpace(first) + " | " + strings.TrimSpace(second)
	}
}

func MakeReportRow(address, field, l1Value, l2Value, mismatchReason, l1Err, l2Err string) ReportRow {
	if err := JoinErrors(l1Err, l2Err); err != "" {
		return ReportRow{
			Address: address,
			Status:  StatusError,
			Field:   field,
			L1Value: l1Value,
			L2Value: l2Value,
			Error:   err,
		}
	}
	if l1Value == l2Value {
		return ReportRow{
			Address: address,
			Status:  StatusMatch,
			Field:   field,
			L1Value: l1Value,
			L2Value: l2Value,
		}
	}
	return ReportRow{
		Address: address,
		Status:  StatusMismatch,
		Field:   field,
		L1Value: l1Value,
		L2Value: l2Value,
		Error:   mismatchReason,
	}
}

func AggregateStatus(rows []ReportRow) Status {
	status := StatusMatch
	for _, row := range rows {
		switch row.Status {
		case StatusError:
			return StatusError
		case StatusMismatch:
			status = StatusMismatch
		}
	}
	return status
}
