package bulkprompt

import (
	_ "embed"
	"strings"
)

// Long-lived platform instructions are build-embedded text assets so prompt
// changes remain reviewable without mixing prompt prose into application code.

//go:embed prompts/platform_v1.txt
var platformContractV1 string

//go:embed prompts/generic_v1.txt
var genericContractV1 string

//go:embed prompts/credit_card_bill_v1.txt
var creditCardBillContractV1 string

func embeddedPrompt(value string) string { return strings.TrimSuffix(value, "\n") }
