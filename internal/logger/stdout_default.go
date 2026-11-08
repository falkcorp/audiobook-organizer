// file: internal/logger/stdout_default.go
// version: 1.1.0
// guid: 4911a492-f35a-4720-a75a-e18ab1bd1dfa
// last-edited: 2026-11-08

package logger

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	corelog "github.com/falkcorp/mediacore/log"
)

// ParseLevelOrInfo reads a log_level / --log-level value with core's parser
// (trace, debug, info, warn, error, any case). "" is info. A level it does not
// know is info too, with warn set to the message to show the operator: a
// stored typo must not stop a command or serve from starting, and must not be
// silent. It is the one place that message is worded; serve's pipeline settings
// (internal/books) and InstallStdoutDefault both use it.
func ParseLevelOrInfo(level string) (lv slog.Level, warn string) {
	l := strings.TrimSpace(level)
	if l == "" {
		return slog.LevelInfo, ""
	}
	lv, err := corelog.ParseLevel(l)
	if err != nil {
		return slog.LevelInfo, fmt.Sprintf("log_level %q is not trace, debug, info, warn or error; using info", level)
	}
	return lv, ""
}

// InstallStdoutDefault makes slog.Default a text handler on standard output at
// level (log_level / --log-level; see ParseLevelOrInfo), and returns the call
// that puts the previous default back. A level ParseLevelOrInfo does not know
// is reported as "Warning: ..." on warnTo, as serve's pipeline settings report
// it on standard error; nil warnTo reports nothing, for a caller whose own
// settings path reports it (serve's, in internal/books).
//
// It is for lines logged where no App has installed its log pipeline: the
// scan and organize commands, which never have one, and serve's startup before
// app.New (config, store open, migrations, key guard), where slog's stock
// default would print to standard error while the rest of serve's output goes
// to standard output. App.InstallProcessDefaults stacks on top of it and its
// restore puts this handler back, which the restore returned here then undoes.
func InstallStdoutDefault(level string, warnTo io.Writer) (restore func()) {
	lv, warn := ParseLevelOrInfo(level)
	if warn != "" && warnTo != nil {
		fmt.Fprintln(warnTo, "Warning: "+warn)
	}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv})))
	return func() { slog.SetDefault(prev) }
}
