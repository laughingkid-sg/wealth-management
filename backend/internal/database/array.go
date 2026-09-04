package database

import (
	"strings"

	"github.com/google/uuid"
)

// TextArrayLiteral returns a PostgreSQL text-array literal. Passing the
// literal as a string keeps array arguments encodable when pgx is using the
// simple query protocol required by Supabase's transaction pooler.
func TextArrayLiteral(values []string) string {
	var result strings.Builder
	result.WriteByte('{')
	for index, value := range values {
		if index > 0 {
			result.WriteByte(',')
		}
		result.WriteByte('"')
		for _, character := range value {
			if character == '"' || character == '\\' {
				result.WriteByte('\\')
			}
			result.WriteRune(character)
		}
		result.WriteByte('"')
	}
	result.WriteByte('}')
	return result.String()
}

// NullableTextArrayLiteral preserves nil as SQL NULL while encoding a
// non-nil slice, including an empty slice, as a PostgreSQL array literal.
func NullableTextArrayLiteral(values []string) any {
	if values == nil {
		return nil
	}
	return TextArrayLiteral(values)
}

// UUIDArrayLiteral returns a PostgreSQL UUID-array literal. UUID strings do
// not need array-element quoting, but reusing TextArrayLiteral also makes the
// representation unambiguous and consistent with other array parameters.
func UUIDArrayLiteral(values []uuid.UUID) string {
	encoded := make([]string, len(values))
	for index, value := range values {
		encoded[index] = value.String()
	}
	return TextArrayLiteral(encoded)
}
