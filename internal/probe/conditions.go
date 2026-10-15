package probe

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// operators in the order they are matched at the same position, so "<=" wins over "<".
var operators = []string{"==", "!=", "<=", ">=", "<", ">"}

type condition struct {
	raw         string
	left, right operand
	op          string
}

type operand struct {
	text        string
	placeholder string
	length      bool
	pattern     bool
}

// parseCondition splits "[OFFSET] < 500ms" into its operands. Placeholders must be in known.
func parseCondition(raw string, known map[string]bool) (condition, error) {
	c := condition{raw: strings.TrimSpace(raw)}
	pos := -1
	for _, op := range operators {
		if i := strings.Index(c.raw, op); i >= 0 && (pos < 0 || i < pos) {
			pos, c.op = i, op
		}
	}
	if pos < 0 {
		return c, errors.New("no operator (==, !=, <, <=, >, >=)")
	}
	var err error
	if c.left, err = parseOperand(c.raw[:pos], known); err != nil {
		return c, err
	}
	if c.right, err = parseOperand(c.raw[pos+len(c.op):], known); err != nil {
		return c, err
	}
	if c.left.text == "" || c.right.text == "" {
		return c, errors.New("an operand is empty")
	}
	if (c.left.pattern || c.right.pattern) && c.op != "==" && c.op != "!=" {
		return c, errors.New("pat() only works with == and !=")
	}
	return c, nil
}

func parseOperand(s string, known map[string]bool) (operand, error) {
	o := operand{text: strings.TrimSpace(s)}
	inner := o.text
	switch {
	case strings.HasPrefix(inner, "len(") && strings.HasSuffix(inner, ")"):
		o.length, inner = true, strings.TrimSpace(inner[4:len(inner)-1])
	case strings.HasPrefix(inner, "pat(") && strings.HasSuffix(inner, ")"):
		o.pattern, o.text = true, inner[4:len(inner)-1]
		return o, nil
	}
	if strings.HasPrefix(inner, "[") && strings.HasSuffix(inner, "]") {
		name := strings.ToUpper(inner[1 : len(inner)-1])
		if !known[name] {
			return o, fmt.Errorf("unknown placeholder [%s]", name)
		}
		o.placeholder = name
		return o, nil
	}
	if o.length {
		return o, errors.New("len() needs a placeholder")
	}
	if len(inner) >= 2 && inner[0] == '"' && inner[len(inner)-1] == '"' {
		o.text = inner[1 : len(inner)-1]
	}
	return o, nil
}

// evaluate returns a Gatus-style description of every failed condition, e.g. "[OFFSET] (812) <= 500ms".
// A condition on a known placeholder without a value yet (BANDWIDTH before the first test) is skipped.
func evaluate(conditions []string, values Values, known map[string]bool) []string {
	var failed []string
	for _, raw := range conditions {
		c, err := parseCondition(raw, known)
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", strings.TrimSpace(raw), err))
			continue
		}
		left, okL := c.left.resolve(values)
		right, okR := c.right.resolve(values)
		if !okL || !okR {
			continue
		}
		if !compare(c, left, right) {
			failed = append(failed, c.describe(values))
		}
	}
	return failed
}

func (o operand) resolve(values Values) (string, bool) {
	if o.placeholder == "" {
		return o.text, true
	}
	v, ok := values[o.placeholder]
	if !ok {
		return "", false
	}
	s := formatValue(v)
	if o.length {
		s = strconv.Itoa(len(s))
	}
	return s, true
}

func compare(c condition, left, right string) bool {
	if c.left.pattern || c.right.pattern {
		pattern, value := right, left
		if c.left.pattern {
			pattern, value = left, right
		}
		return wildcard(pattern, value) == (c.op == "==")
	}
	l, lok := number(left)
	r, rok := number(right)
	if lok && rok {
		switch c.op {
		case "==":
			return l == r
		case "!=":
			return l != r
		case "<":
			return l < r
		case "<=":
			return l <= r
		case ">":
			return l > r
		case ">=":
			return l >= r
		}
	}
	switch c.op {
	case "==":
		return strings.EqualFold(left, right)
	case "!=":
		return !strings.EqualFold(left, right)
	}
	return false
}

// number reads numbers and durations; durations become milliseconds, like the time placeholders.
func number(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f, true
	}
	if d, err := time.ParseDuration(s); err == nil {
		return float64(d) / float64(time.Millisecond), true
	}
	return 0, false
}

func (c condition) describe(values Values) string {
	side := func(o operand) string {
		if o.placeholder == "" {
			return o.text
		}
		v, _ := o.resolve(values)
		if o.length {
			return fmt.Sprintf("len([%s]) (%s)", o.placeholder, v)
		}
		return fmt.Sprintf("[%s] (%s)", o.placeholder, v)
	}
	return side(c.left) + " " + c.op + " " + side(c.right)
}

// wildcard matches * against any text, like Gatus pat().
func wildcard(pattern, s string) bool {
	parts := strings.Split(pattern, "*")
	if len(parts) == 1 {
		return pattern == s
	}
	if !strings.HasPrefix(s, parts[0]) {
		return false
	}
	s = s[len(parts[0]):]
	for _, p := range parts[1 : len(parts)-1] {
		i := strings.Index(s, p)
		if i < 0 {
			return false
		}
		s = s[i+len(p):]
	}
	return strings.HasSuffix(s, parts[len(parts)-1])
}
