// Package prompts owns immutable model instructions that are embedded in the
// backend binary at build time. Keeping these assets outside Go source makes
// prompt review possible without making platform guardrails runtime-editable.
package prompts

import (
	_ "embed"
	"strings"
)

// TransactionParserVersion changes whenever the immutable platform prompt's
// meaning changes. Configurable source and user fragments are versioned by the
// database instead.
const TransactionParserVersion = 2

//go:embed transactions/system_v2.txt
var transactionParserSystemPrompt string

// TransactionParserSystem returns the build-embedded platform prompt without
// the text file's conventional final newline.
func TransactionParserSystem() string {
	return strings.TrimSuffix(transactionParserSystemPrompt, "\n")
}
