// Package function provides shell, text-file, and image tools for provider.Client.
// These tools run with the process's permissions, without a sandbox or approval
// step. Callers must establish any execution restrictions before using them.
//
// Bash is ready to use as a zero value. Create Read, View, Write, and Edit with
// NewFileFunctions; their zero values are not usable. File tools accept regular
// files, reject symlinks as the final path component, and resolve relative paths
// against the process's working directory.
//
// Each tool's Schema describes its arguments. For direct Call invocations, use
// encoding/json.Number for numeric values and valid UTF-8 for strings. Nullable
// arguments may be omitted or nil to select defaults. Calls report tool failures
// in their text results; View also returns an image part on success.
package function
