//go:build windows

package ocr

import "os/exec"

// setProcGroup is a no-op on Windows. exec.CommandContext already terminates the
// child renderer when the context is cancelled; process-group kill semantics
// differ on Windows and the renderers used here do not spawn grandchildren.
func setProcGroup(cmd *exec.Cmd) {}
