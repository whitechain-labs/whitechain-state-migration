package tokensync

import (
	"context"
	"errors"
	"strings"
	"testing"

	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestBuildTokenRecordUsesConfiguredType(t *testing.T) {
	t.Parallel()

	record, err := buildTokenRecord(
		tokenTarget{
			Address: "0x0000000000000000000000000000000000000011",
			Type:    tokenKind("ERC-721"),
		},
		&explorerToken{
			Type:        "wrc20",
			Contract:    "0x0000000000000000000000000000000000000011",
			Name:        "Wrapped WBT",
			Symbol:      "WWBT",
			TotalSupply: "5",
			Decimals:    0,
		},
	)
	if err != nil {
		t.Fatalf("buildTokenRecord() error = %v", err)
	}

	if record.Type != tokenKind("ERC-721") {
		t.Fatalf("record.Type = %q, want %q", record.Type, tokenKind("ERC-721"))
	}
}

func TestBuildTokenRecordRejectsMismatchedContract(t *testing.T) {
	t.Parallel()

	_, err := buildTokenRecord(
		tokenTarget{
			Address: "0x0000000000000000000000000000000000000011",
			Type:    tokenKind("ERC-20"),
		},
		&explorerToken{
			Contract:    "0x0000000000000000000000000000000000000099",
			TotalSupply: "1",
			Decimals:    0,
		},
	)
	if err == nil {
		t.Fatal("buildTokenRecord() error = nil, want mismatch error")
	}
}

func TestBuildTokenRecordRejectsExcessiveDecimals(t *testing.T) {
	t.Parallel()

	_, err := buildTokenRecord(
		tokenTarget{
			Address: "0x0000000000000000000000000000000000000011",
			Type:    tokenKind("ERC-20"),
		},
		&explorerToken{
			Contract:    "0x0000000000000000000000000000000000000011",
			TotalSupply: "1",
			Decimals:    maxTokenDecimals + 1,
		},
	)
	if err == nil {
		t.Fatal("buildTokenRecord() error = nil, want invalid decimals error")
	}
	if !strings.Contains(err.Error(), "invalid decimals") {
		t.Fatalf("buildTokenRecord() error = %v, want invalid decimals message", err)
	}
}

func TestCloseStoreLogsCloseError(t *testing.T) {
	t.Parallel()

	logger, hook := logtest.NewNullLogger()
	previous := log.StandardLogger().Out
	previousFormatter := log.StandardLogger().Formatter
	previousLevel := log.StandardLogger().Level
	previousHooks := log.StandardLogger().Hooks
	log.SetOutput(logger.Out)
	log.SetFormatter(logger.Formatter)
	log.SetLevel(logger.Level)
	log.StandardLogger().ReplaceHooks(logger.Hooks)
	defer func() {
		log.SetOutput(previous)
		log.SetFormatter(previousFormatter)
		log.SetLevel(previousLevel)
		log.StandardLogger().ReplaceHooks(previousHooks)
	}()

	closeErr := errors.New("close failed")
	closeStore(context.Background(), testStoreCloser{err: closeErr})

	entries := hook.AllEntries()
	if len(entries) != 1 {
		t.Fatalf("logged entries = %d, want 1", len(entries))
	}
	if entries[0].Level != log.WarnLevel {
		t.Fatalf("log level = %v, want %v", entries[0].Level, log.WarnLevel)
	}
	if entries[0].Message != "close tokensync store" {
		t.Fatalf("log message = %q, want %q", entries[0].Message, "close tokensync store")
	}
	if entries[0].Data[log.ErrorKey] != closeErr {
		t.Fatalf("logged error = %v, want %v", entries[0].Data[log.ErrorKey], closeErr)
	}
}

type testStoreCloser struct {
	err error
}

func (c testStoreCloser) Close(context.Context) error {
	return c.err
}

func TestResolveSyncAction(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name     string
		existing *existingToken
		force    bool
		want     syncAction
		wantMsg  string
	}{
		{
			name:  "insert when token does not exist",
			want:  syncActionInsert,
			force: false,
		},
		{
			name: "skip existing without force",
			existing: &existingToken{
				HolderCount:   0,
				TransferCount: 0,
			},
			want:    syncActionSkip,
			force:   false,
			wantMsg: "token already added, skipping",
		},
		{
			name: "update existing with force and empty counters",
			existing: &existingToken{
				HolderCount:   0,
				TransferCount: 0,
			},
			want:  syncActionUpdate,
			force: true,
		},
		{
			name: "skip force when holders exist",
			existing: &existingToken{
				HolderCount:   1,
				TransferCount: 0,
			},
			want:    syncActionSkip,
			force:   true,
			wantMsg: "token already has holder_count=1 and transfer_count=0 in Blockscout, skipping force update",
		},
		{
			name: "skip force when transfers exist",
			existing: &existingToken{
				HolderCount:   0,
				TransferCount: 2,
			},
			want:    syncActionSkip,
			force:   true,
			wantMsg: "token already has holder_count=0 and transfer_count=2 in Blockscout, skipping force update",
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, msg := resolveSyncAction(tc.existing, tc.force)
			if got != tc.want {
				t.Fatalf("resolveSyncAction() = %q, want %q", got, tc.want)
			}
			if msg != tc.wantMsg {
				t.Fatalf("resolveSyncAction() message = %q, want %q", msg, tc.wantMsg)
			}
		})
	}
}
