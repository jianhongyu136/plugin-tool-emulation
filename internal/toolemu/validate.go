package toolemu

// ValidateDeclaredToolNames rejects hallucinated names even under auto choice.
func ValidateDeclaredToolNames(parsed Parsed, tools []ToolSpec) error {
	for _, call := range parsed.ToolCalls {
		declared := false
		for _, tool := range tools {
			if tool.Name == call.Name {
				declared = true
				break
			}
		}
		if !declared {
			return ToolChoiceValidationError{Parsed: parsed, Reason: "toolemu: response used an undeclared tool"}
		}
	}
	return nil
}
