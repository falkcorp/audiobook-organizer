// file: core/app/offline_journal_queue.go
// version: 0.1.0
// guid: b17d4e90-52c3-4a6f-8d19-e0a3c7f5b624
// last-edited: 2026-11-21

package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/falkcorp/mediacore/kernel"
	"github.com/falkcorp/mediacore/module"
)

// storage journal-queue (F4-08 Q6a; 05-storage §5S.4 "Offline open" rule 4):
// the operator's tool for the queue of revocation lines this build could not
// apply (c/backup/journal_unapplied/<seq>). A row of it that does not decode
// fails every boot, and a row for a line the journal no longer holds sits in
// the queue for good; `list` shows each row and what it is, `clear <seq>
// --confirm` removes one that is poison or provably orphaned, audited, and
// refuses every other, because an entry the journal still holds is a
// revocation the store is owed.

const journalQueueName = "storage journal-queue"

const journalQueueUsage = "storage journal-queue list | clear <seq> --confirm"

const journalQueueLong = `Lists, or removes, entries of the unapplied-revocation queue, with the service stopped.

  storage journal-queue list
  storage journal-queue clear <seq> --confirm

The queue holds a revocation line this build did not know how to apply (a
newer build wrote it). The first build that knows its kind applies it and
removes the entry. The command opens the store as every offline command does:
it refuses a running service, a staged or in-flight restore, and a state
directory that is not yours.

list prints one line per row: seq=, kind=, at=, closed=, state= and why. The
states are

  pending     the journal holds the line; a build that knows its kind applies
              it at its next boot. Cannot be cleared.
  orphan      the journal verified clean and holds no such line, so nothing can
              ever apply the entry. Can be cleared.
  poison      the row does not decode, or its key and entry disagree; every
              boot that reads the queue fails on it. Can be cleared.
  unverified  the journal is missing, unreadable, broken, another install's or
              disagrees with the store, so it cannot be shown whether the entry
              is owed. Cannot be cleared.
  malformed_key  the key names no seq; listed only.

clear removes the one entry at <seq> when it is orphan or poison, in one batch
with an audit record (core.journal_queue.clear, method offline), and refuses
any other state, changing nothing. It requires --confirm: the entry is gone
afterwards. A pending entry is never cleared by this command: boot a build that
knows the kind, or restore the journal.`

// journalQueueCommand is storage journal-queue: one command whose first
// argument is the verb, as storage cutover-blockers is.
func journalQueueCommand(opts Options, mods []module.Module) Command {
	return Command{
		Name:  journalQueueName,
		Short: "List or clear the unapplied-revocation queue, with the service stopped",
		Long:  journalQueueLong,
		Run: func(ctx context.Context, args []string, stdout io.Writer) error {
			if len(args) == 0 {
				return fmt.Errorf("%s: a verb is required: list or clear (usage: %s)", journalQueueName, journalQueueUsage)
			}
			verb, rest := args[0], args[1:]
			switch verb {
			case "-h", "-help", "--help", "help":
				_, err := fmt.Fprintf(stdout, "Usage: %s\n\n%s\n", journalQueueUsage, journalQueueLong)
				return err
			case "list":
				return runJournalQueueList(ctx, opts, mods, rest, stdout)
			case "clear":
				return runJournalQueueClear(ctx, opts, mods, rest, stdout)
			}
			return fmt.Errorf("%s: unknown verb %q: want list or clear", journalQueueName, verb)
		},
	}
}

func runJournalQueueList(ctx context.Context, opts Options, mods []module.Module, args []string, stdout io.Writer) error {
	fl := flag.NewFlagSet(journalQueueName+" list", flag.ContinueOnError)
	if help, err := parseFlags(fl, args, stdout, "storage journal-queue list", journalQueueLong); help || err != nil {
		return err
	}
	c, err := offlineConfig(ctx, opts, mods, readWriteOwner(false))
	if err != nil {
		return err
	}
	entries, err := kernel.JournalQueueList(ctx, c)
	if err != nil {
		return fmt.Errorf("%s list: %w", journalQueueName, err)
	}
	if len(entries) == 0 {
		_, err = io.WriteString(stdout, "The unapplied-revocation queue is empty.\n")
		return err
	}
	var out strings.Builder
	for _, e := range entries {
		if e.State == kernel.QueueMalformedKey {
			fmt.Fprintf(&out, "key=%q state=%s: %s\n", e.Key, e.State, e.Reason)
			continue
		}
		kind, at := string(e.Kind), e.At.UTC().Format("2006-01-02T15:04:05Z")
		if e.Kind == "" {
			kind, at = "-", "-"
		}
		fmt.Fprintf(&out, "seq=%d kind=%s at=%s closed=%t state=%s: %s\n", e.Seq, kind, at, e.Closed, e.State, e.Reason)
	}
	fmt.Fprintf(&out, "%d entr%s.\n", len(entries), plural(len(entries), "y", "ies"))
	_, err = io.WriteString(stdout, out.String())
	return err
}

func runJournalQueueClear(ctx context.Context, opts Options, mods []module.Module, args []string, stdout io.Writer) (err error) {
	seqArg, flagArgs := splitLeadingArg(args)
	fl := flag.NewFlagSet(journalQueueName+" clear", flag.ContinueOnError)
	confirm := fl.Bool("confirm", false, "remove the entry (required)")
	if help, err := parseFlags(fl, flagArgs, stdout, "storage journal-queue clear <seq> --confirm", journalQueueLong); help || err != nil {
		return err
	}
	if seqArg == "" {
		return fmt.Errorf("%s clear: %w", journalQueueName, errNoSeq)
	}
	seq, err := strconv.ParseUint(seqArg, 10, 64)
	if err != nil || seq == 0 {
		return fmt.Errorf("%s clear: %q is not a seq (a positive integer, as list prints it)", journalQueueName, seqArg)
	}
	// Before the store is opened: a refused clear touches nothing.
	if !*confirm {
		return fmt.Errorf("%s clear: --confirm is required; the entry is removed (run list first to see what it is)", journalQueueName)
	}
	c, err := offlineConfig(ctx, opts, mods, readWriteOwner(false))
	if err != nil {
		return err
	}
	e, err := kernel.JournalQueueClear(ctx, c, seq, operatorName())
	if err != nil {
		if errors.Is(err, kernel.ErrJournalQueueNotClearable) || errors.Is(err, kernel.ErrJournalQueueNoEntry) {
			return fmt.Errorf("%s clear: %w; nothing was changed", journalQueueName, err)
		}
		return fmt.Errorf("%s clear: %w", journalQueueName, err)
	}
	_, err = fmt.Fprintf(stdout, "Cleared the %s entry at seq %d.\n", e.State, e.Seq)
	return err
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
