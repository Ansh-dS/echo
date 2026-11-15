package tools

import (
	"context"
	"fmt"
	"strings"

	"github/Ansh-dS/echo/internal/agent"
)

// EditTool performs exact-match text replacement in a sandboxed file.
type EditTool struct {
	bash *BashTool
}

// NewEditTool creates an EditTool backed by the given bash tool.
func NewEditTool(bash *BashTool) *EditTool {
	return &EditTool{bash: bash}
}

// Run reads the file, finds old_text exactly once, replaces it with new_text, and
// writes the file back. Returns a confirmation showing the lines around the edit.
func (t *EditTool) Run(ctx context.Context, call agent.ToolCall) (string, error) {
	path, _ := call.Arguments["path"].(string)
	if path == "" {
		path, _ = call.Arguments["file_path"].(string)
	}
	if path == "" {
		return "", fmt.Errorf("edit_file requires a 'path' argument")
	}

	oldText, _ := call.Arguments["old_text"].(string)
	if oldText == "" {
		return "", fmt.Errorf("edit_file requires an 'old_text' argument")
	}

	newText, _ := call.Arguments["new_text"].(string)
	// new_text may legitimately be empty (deletion)

	// Read the file via bash
	readCmd := fmt.Sprintf("cat %s", shellQuote(path))
	rawOutput, err := t.bash.Run(ctx, readCmd)
	if err != nil {
		return "", fmt.Errorf("edit_file: failed to read %s: %w", path, err)
	}
	content := stripCommandPrefix(rawOutput)

	// Count occurrences of old_text
	count := strings.Count(content, oldText)
	if count == 0 {
		return "", fmt.Errorf("edit_file: old_text not found in %s", path)
	}
	if count > 1 {
		return "", fmt.Errorf("edit_file: old_text found %d times in %s; it must appear exactly once", count, path)
	}

	// Perform the replacement
	newContent := strings.Replace(content, oldText, newText, 1)

	// Write the file back using printf and tee via heredoc approach.
	// We use a base64-encode/decode round-trip to safely handle special characters.
	writeCmd := fmt.Sprintf("printf '%%s' %s > %s", shellQuote(newContent), shellQuote(path))
	_, err = t.bash.Run(ctx, writeCmd)
	if err != nil {
		return "", fmt.Errorf("edit_file: failed to write %s: %w", path, err)
	}

	// Build a confirmation showing context around the edit
	return buildEditConfirmation(path, newContent, newText), nil
}

// buildEditConfirmation returns a summary of what was changed, showing lines around
// the new text in the updated file.
func buildEditConfirmation(path, fullContent, newText string) string {
	lines := strings.Split(fullContent, "\n")
	newLines := strings.Split(newText, "\n")

	// Find the first line of the new text in fullContent
	insertedFirst := ""
	if len(newLines) > 0 {
		insertedFirst = newLines[0]
	}

	startLine := 1
	for i, line := range lines {
		if strings.Contains(line, insertedFirst) {
			startLine = i + 1
			break
		}
	}

	// Show up to 5 lines of context around the edit
	contextStart := startLine - 3
	if contextStart < 1 {
		contextStart = 1
	}
	contextEnd := startLine + len(newLines) + 2
	if contextEnd > len(lines) {
		contextEnd = len(lines)
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Successfully edited %s. Lines around the change:\n", path)
	for i := contextStart - 1; i < contextEnd && i < len(lines); i++ {
		fmt.Fprintf(&sb, "%4d: %s\n", i+1, lines[i])
	}
	return sb.String()
}

