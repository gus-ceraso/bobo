package function

import (
	"encoding/json"
	"fmt"
	"math"
	"unicode/utf8"
)

func stringArgument(args map[string]any, name string) (string, error) {
	v, ok := args[name]
	if !ok {
		return "", fmt.Errorf("invalid arguments: missing %s", name)
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("invalid arguments: %s is not a string", name)
	}
	if !utf8.ValidString(s) {
		return "", fmt.Errorf("invalid arguments: invalid utf-8")
	}
	return s, nil
}

func nonemptyStringArgument(args map[string]any, name string) (string, error) {
	s, err := stringArgument(args, name)
	if err != nil {
		return "", err
	}
	if s == "" {
		return "", fmt.Errorf("invalid arguments: %s is empty", name)
	}
	return s, nil
}

func boolArgument(args map[string]any, name string) (bool, error) {
	v := args[name]
	if v == nil {
		return false, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("invalid arguments: %s is not a bool", name)
	}
	return b, nil
}

func positiveIntArgument(args map[string]any, name string, fallback int) (int, error) {
	v := args[name]
	if v == nil {
		return fallback, nil
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, fmt.Errorf("invalid arguments: %s is not an integer", name)
	}
	i, err := n.Int64()
	if err != nil {
		return 0, fmt.Errorf("invalid arguments: %s is not a valid integer: %w", name, err)
	}
	if i < 1 || i > math.MaxInt {
		return 0, fmt.Errorf("invalid arguments: %s is out of range", name)
	}
	return int(i), nil
}

func numberArgument(args map[string]any, name string, fallback float64) (float64, error) {
	v := args[name]
	if v == nil {
		return fallback, nil
	}
	n, ok := v.(json.Number)
	if !ok {
		return 0, fmt.Errorf("invalid arguments: %s is not a number", name)
	}
	f, err := n.Float64()
	if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, fmt.Errorf("invalid arguments: %s must be finite", name)
	}
	return f, nil
}

func positiveNumberArgument(args map[string]any, name string, fallback float64) (float64, error) {
	f, err := numberArgument(args, name, fallback)
	if err != nil {
		return 0, err
	}
	if f <= 0 {
		return 0, fmt.Errorf("invalid arguments: %s must be greater than 0", name)
	}
	return f, nil
}

func boundedNumberArgument(args map[string]any, name string, fallback, low, high float64) (float64, error) {
	f, err := numberArgument(args, name, fallback)
	if err != nil {
		return 0, err
	}
	if f < low || f > high {
		return 0, fmt.Errorf("invalid arguments: %s must be between %g and %g", name, low, high)
	}
	return f, nil
}
