//go:build linux

package main

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func isTerminal(file *os.File) bool {
	_, err := unix.IoctlGetTermios(int(file.Fd()), unix.TCGETS)
	return err == nil
}

func printError(w io.Writer, err error) {
	if file, ok := w.(*os.File); ok && isTerminal(file) {
		fmt.Fprintf(w, "\x1b[31m%s\x1b[0m\n", err)
	} else {
		fmt.Fprintln(w, err)
	}
}
