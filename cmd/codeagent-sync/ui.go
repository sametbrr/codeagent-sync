package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/AlecAivazis/survey/v2"
)

// ANSI colors; empty unless output goes to a terminal and NO_COLOR is unset.
var colorReset, colorBold, colorDim, colorRed, colorGreen, colorYellow, colorCyan string

func initColors(w io.Writer) {
	if os.Getenv("NO_COLOR") != "" || !isTerminal(w) {
		return
	}
	colorReset, colorBold, colorDim = "\033[0m", "\033[1m", "\033[2m"
	colorRed, colorGreen, colorYellow, colorCyan = "\033[31m", "\033[32m", "\033[33m", "\033[36m"
}

func isTerminal(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// interactive reports whether questions can be asked.
func (a *app) interactive() bool { return isTerminal(a.in) && isTerminal(a.out) }

func (a *app) printf(format string, args ...any) {
	if !a.quiet && !a.jsonOut {
		fmt.Fprintf(a.out, format, args...)
	}
}

func (a *app) info(format string, args ...any) {
	a.printf("  "+colorDim+format+colorReset+"\n", args...)
}

func (a *app) success(format string, args ...any) {
	a.printf(colorGreen+"✓"+colorReset+" "+format+"\n", args...)
}

// warn goes to stderr, also in quiet mode: it needs attention.
func (a *app) warn(format string, args ...any) {
	fmt.Fprintf(a.errOut, colorYellow+"!"+colorReset+" "+format+"\n", args...)
}

var errNotInteractive = errors.New("this needs an answer; run it in a terminal or pass --yes")

// confirm asks a yes/no question; assumeYes answers it without asking.
func (a *app) confirm(question string, def, assumeYes bool) (bool, error) {
	if assumeYes {
		return true, nil
	}
	if !a.interactive() {
		return false, errNotInteractive
	}
	answer := def
	err := survey.AskOne(&survey.Confirm{Message: question, Default: def}, &answer)
	return answer, err
}
