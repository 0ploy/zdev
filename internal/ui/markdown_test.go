package ui

import (
	"strings"
	"testing"
)

func TestRenderMarkdown(t *testing.T) {
	tests := []struct {
		name            string
		input           string
		plainMode       bool
		wantContains    []string
		wantNotContains []string
	}{
		{
			name:            "heading removal",
			input:           "## My Heading\n",
			plainMode:       false,
			wantContains:    []string{"My Heading", ansiGreen, ansiBold},
			wantNotContains: []string{"##"},
		},
		{
			name:            "h3 heading",
			input:           "### Subheading\n",
			plainMode:       false,
			wantContains:    []string{"Subheading", ansiYellow, ansiBold},
			wantNotContains: []string{"###"},
		},
		{
			name:            "inline code",
			input:           "Use `code` here\n",
			plainMode:       false,
			wantContains:    []string{"code", ansiCyan},
			wantNotContains: []string{"`"},
		},
		{
			name:            "code block",
			input:           "```bash\necho hello\n```\n",
			plainMode:       false,
			wantContains:    []string{"echo hello", ansiCyan},
			wantNotContains: []string{"```"},
		},
		{
			name:            "bold text",
			input:           "This is **bold** text\n",
			plainMode:       false,
			wantContains:    []string{"bold", ansiBold},
			wantNotContains: []string{"**"},
		},
		{
			name:            "plain mode returns input unchanged",
			input:           "## Heading\n`code`\n",
			plainMode:       true,
			wantContains:    []string{"##", "`code`"},
			wantNotContains: []string{ansiGreen, ansiCyan},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RenderMarkdown(tt.input, tt.plainMode)

			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Errorf("RenderMarkdown() should contain %q, got %q", want, got)
				}
			}

			for _, notWant := range tt.wantNotContains {
				if strings.Contains(got, notWant) {
					t.Errorf("RenderMarkdown() should not contain %q, got %q", notWant, got)
				}
			}
		})
	}
}
