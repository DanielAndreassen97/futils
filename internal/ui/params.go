package ui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/DanielAndreassen97/futils/internal/fabric"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// paramFormTheme mirrors the themeing used by Confirm() so parameter
// forms look visually continuous with the rest of the TUI.
func paramFormTheme() *huh.Theme {
	t := huh.ThemeBase()
	t.Focused.Title = lipgloss.NewStyle().Foreground(AccentColor).Bold(true)
	t.Focused.Description = lipgloss.NewStyle().Foreground(DimColor)
	t.Focused.FocusedButton = lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(AccentColor).Padding(0, 1)
	t.Focused.BlurredButton = lipgloss.NewStyle().Foreground(DimColor).Padding(0, 1)
	t.Focused.TextInput.Cursor = lipgloss.NewStyle().Foreground(AccentColor)
	t.Focused.TextInput.Prompt = lipgloss.NewStyle().Foreground(AccentColor)
	// Preserve the base-theme cursor glyph; only override its colour.
	// Replacing the whole style wipes the SetString("> ") and makes
	// the selector invisible.
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(AccentColor).Bold(true)
	return t
}

// ParameterForm prompts the user to override each discovered notebook
// parameter. The form renders:
//
//   - Text / Int / Float → free-text input, empty means "keep notebook default"
//   - Bool               → Yes/No confirm, pre-set to the notebook's default
//
// The return value contains ONLY genuine overrides. Fields the user
// didn't change are omitted so Fabric falls back to the notebook's own
// Python default — and because Fabric rejects empty-string Text values
// with a 400, we must NOT send them.
//
// Returns ErrGoBack if the user presses esc, ErrQuit on ctrl+c.
// ParameterForm renders an override form for the given parameters.
//
// prefill picks the empty-value semantics, which differ by flow:
//   - false (notebooks): text fields start EMPTY with the default shown as a
//     placeholder, and leaving a field empty means "keep the default". A
//     notebook can't distinguish an omitted parameter from an empty one, so
//     this is the safe default.
//   - true (pipelines): text fields are PRE-FILLED with the default as real
//     editable text, so the box is WYSIWYG — whatever it holds is what's sent.
//     Leaving it sends the default (identical to omitting it, since Fabric
//     applies the pipeline's default server-side); CLEARING it sends an
//     explicit empty string, which some pipeline parameters legitimately want.
func ParameterForm(params []fabric.Parameter, prefill bool) ([]fabric.JobInput, error) {
	if len(params) == 0 {
		return nil, nil
	}

	// Parallel storage — huh binds field values via pointers, so we need
	// concrete variables with lifetimes spanning the Run() call.
	textValues := make([]string, len(params))
	boolValues := make([]bool, len(params))
	groups := make([]*huh.Group, 0, len(params))

	for i, p := range params {
		var field huh.Field
		switch p.Type {
		case fabric.TypeBool:
			if d, ok := p.Default.(bool); ok {
				boolValues[i] = d
			}
			field = huh.NewConfirm().
				Title(p.Name).
				Description(fmt.Sprintf("default: %s", p.RawDefault)).
				Affirmative("True").
				Negative("False").
				Value(&boolValues[i])

		default:
			desc := "optional"
			if p.RawDefault != "" && p.RawDefault != "''" {
				desc = fmt.Sprintf("default: %s", p.RawDefault)
			}
			// WYSIWYG prefill must seed the bound variable BEFORE Value() —
			// huh captures the current *ptr at Value() call time, so seeding
			// afterwards is silently ignored (the field renders empty).
			if prefill {
				textValues[i] = p.RawDefault
			}
			input := huh.NewInput().
				Title(p.Name).
				Validate(validatorFor(p.Type)).
				Value(&textValues[i])
			if prefill {
				// The box IS the value; clearing it sends an explicit empty string.
				input = input.Description(desc + " · clear to send an empty value")
			} else {
				// Empty = keep default; show the default as a placeholder.
				input = input.Description(desc).Placeholder(p.RawDefault)
			}
			field = input
		}
		groups = append(groups, huh.NewGroup(field))
	}

	km := huh.NewDefaultKeyMap()
	km.Quit = key.NewBinding(key.WithKeys("ctrl+c", "esc"))

	err := huh.NewForm(groups...).
		WithTheme(paramFormTheme()).
		WithKeyMap(km).
		Run()
	if err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return nil, ErrGoBack
		}
		return nil, err
	}

	return collectOverrides(params, textValues, boolValues, prefill)
}

// collectOverrides walks the form results and emits JobInput entries only
// where the user's value differs from the default. prefill mirrors
// ParameterForm's empty-value semantics for string parameters (see below).
func collectOverrides(params []fabric.Parameter, textValues []string, boolValues []bool, prefill bool) ([]fabric.JobInput, error) {
	var out []fabric.JobInput
	for i, p := range params {
		switch p.Type {
		case fabric.TypeBool:
			def, _ := p.Default.(bool)
			if boolValues[i] == def {
				continue
			}
			out = append(out, fabric.JobInput{Name: p.Name, Value: boolValues[i], Type: p.Type})

		case fabric.TypeInt:
			raw := strings.TrimSpace(textValues[i])
			if raw == "" {
				continue
			}
			v, err := strconv.ParseInt(raw, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p.Name, err)
			}
			if def, ok := p.Default.(int64); ok && v == def {
				continue
			}
			out = append(out, fabric.JobInput{Name: p.Name, Value: v, Type: p.Type})

		case fabric.TypeFloat:
			raw := strings.TrimSpace(textValues[i])
			if raw == "" {
				continue
			}
			v, err := strconv.ParseFloat(raw, 64)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", p.Name, err)
			}
			if def, ok := p.Default.(float64); ok && v == def {
				continue
			}
			out = append(out, fabric.JobInput{Name: p.Name, Value: v, Type: p.Type})

		case fabric.TypeString:
			raw := textValues[i]
			defStr, _ := p.Default.(string)
			if prefill {
				// WYSIWYG: the box holds the default (or "" when none was
				// declared). Unchanged → omit (server keeps the default);
				// anything else — including a cleared, explicitly empty box —
				// is sent verbatim, so an empty string is expressible.
				if raw == defStr {
					continue
				}
				out = append(out, fabric.JobInput{Name: p.Name, Value: raw, Type: p.Type})
				continue
			}
			// Placeholder mode: empty means keep the default.
			if raw == "" || raw == defStr {
				continue
			}
			out = append(out, fabric.JobInput{Name: p.Name, Value: raw, Type: p.Type})
		}
	}
	return out, nil
}

// validatorFor returns a huh.Input validator that accepts empty strings
// (meaning "keep default") and typed values for the given fabric type.
func validatorFor(typ string) func(string) error {
	switch typ {
	case fabric.TypeInt:
		return func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return nil
			}
			if _, err := strconv.ParseInt(s, 10, 64); err != nil {
				return fmt.Errorf("must be an integer")
			}
			return nil
		}
	case fabric.TypeFloat:
		return func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return nil
			}
			if _, err := strconv.ParseFloat(s, 64); err != nil {
				return fmt.Errorf("must be a number")
			}
			return nil
		}
	default:
		return func(string) error { return nil }
	}
}
