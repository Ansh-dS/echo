package tools

import (
	"context"
	"fmt"
	"strings"

	"github/Ansh-dS/echo/internal/agent"
)

// ReadTool reads a file from the sandbox within a specified line range.
type ReadTool struct {
	bash *BashTool
}

// NewReadTool creates a ReadTool backed by the given bash tool.
func NewReadTool(bash *BashTool) *ReadTool {
	return &ReadTool{bash: bash}
}

// Run reads the file at path with an explicit line range from Arguments.
// It rejects requests without a line range or with a range >200 lines.
// Output lines are prefixed with their line numbers.
func (t *ReadTool) Run(ctx context.Context, call agent.ToolCall) (string, error) {
	path, _ := call.Arguments["path"].(string)
	if path == "" {
		path, _ = call.Arguments["file_path"].(string)
	}
	if path == "" {
		return "", fmt.Errorf("read_file requires a 'path' argument")
	}

	// Extract line range — both must be provided
	startLineRaw, hasStart := call.Arguments["start_line"]
	endLineRaw, hasEnd := call.Arguments["end_line"]

	if !hasStart || !hasEnd {
		return "", fmt.Errorf("read_file requires both 'start_line' and 'end_line' arguments")
	}

	startLine := toInt(startLineRaw)
	endLine := toInt(endLineRaw)

	if startLine <= 0 || endLine <= 0 {
		return "", fmt.Errorf("read_file: start_line and end_line must be positive integers")
	}

	if endLine < startLine {
		return "", fmt.Errorf("read_file: end_line (%d) must be >= start_line (%d)", endLine, startLine)
	}

	lineCount := endLine - startLine + 1
	if lineCount > 200 {
		return "", fmt.Errorf("read_file: requested range of %d lines exceeds the 200-line limit; narrow your range", lineCount)
	}

	// Use sed to extract the specific line range
	cmd := fmt.Sprintf("sed -n '%d,%dp' %s", startLine, endLine, shellQuote(path))
	output, err := t.bash.Run(ctx, cmd)
	if err != nil {
		return "", fmt.Errorf("read_file failed for %s: %w", path, err)
	}

	// Strip the "Command succeeded." prefix added by bash.Run
	output = stripCommandPrefix(output)

	return prefixWithLineNumbers(output, startLine), nil
}

// prefixWithLineNumbers adds "NNN: " prefix to each line.
func prefixWithLineNumbers(output string, startLine int) string {
	if output == "" {
		return ""
	}

	lines := strings.Split(output, "\n")
	// Drop trailing empty line from the split
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}

	var sb strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&sb, "%4d: %s\n", startLine+i, line)
	}
	return sb.String()
}

// toInt converts common JSON number types to int.
func toInt(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case float32:
		return int(n)
	}
	return 0
}

// shellQuote wraps a path in single quotes and escapes embedded single quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// stripCommandPrefix removes the "Command succeeded.\n" or "Command failed.\n" prefix
// that BashTool.Run prepends to output.
func stripCommandPrefix(output string) string {
	for _, prefix := range []string{"Command succeeded.\n", "Command failed.\n"} {
		if strings.HasPrefix(output, prefix) {
			return output[len(prefix):]
		}
	}
	// Also handle "Command succeeded with no output."
	if output == "Command succeeded with no output." {
		return ""
	}
	return output
}

