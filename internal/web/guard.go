package web

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/fuadarradhi/kiya/internal/util"
)

var (
	reFormTag    = regexp.MustCompile(`(?i)<form\b[^>]*>`)
	reMethodAttr = regexp.MustCompile(`(?i)method\s*=\s*["']?\s*(\w+)`)
	reHeadTag    = regexp.MustCompile(`(?i)<head\b[^>]*>`)
)

func InjectCSRFIntoForms(html string, token string) string {
	if token == "" {
		return html
	}

	if strings.Contains(html, `name="csrf_token"`) ||
		strings.Contains(html, `name='csrf_token'`) {
		return html
	}

	escapedToken := util.HTMLEscape(token)
	csrfInput := fmt.Sprintf(
		`<input type="hidden" name="csrf_token" value="%s">`,
		escapedToken,
	)

	return reFormTag.ReplaceAllStringFunc(html, func(match string) string {
		methodMatches := reMethodAttr.FindStringSubmatch(match)

		method := "GET"
		if len(methodMatches) >= 2 && methodMatches[1] != "" {
			method = strings.ToUpper(methodMatches[1])
		}

		if method == "GET" {
			return match
		}

		return match + csrfInput
	})
}

func InjectCSRFMeta(html string, token string) string {
	if token == "" {
		return html
	}

	if strings.Contains(html, `name="csrf-token"`) ||
		strings.Contains(html, `name='csrf-token'`) {
		return html
	}

	escapedToken := util.HTMLEscape(token)
	meta := fmt.Sprintf(
		`<meta name="csrf-token" content="%s">`,
		escapedToken,
	)

	return reHeadTag.ReplaceAllStringFunc(html, func(match string) string {
		return match + meta
	})
}

func InjectHoneypotIntoForms(html string, fieldName string) string {
	if fieldName == "" {
		return html
	}

	if strings.Contains(html, fmt.Sprintf(`name="%s"`, fieldName)) ||
		strings.Contains(html, fmt.Sprintf(`name='%s'`, fieldName)) {
		return html
	}

	escapedFieldName := util.HTMLEscape(fieldName)
	hpInput := fmt.Sprintf(
		`<input type="text" name="%s" value="" style="display:none !important; position:absolute !important; left:-9999px !important;" tabindex="-1" autocomplete="off" aria-hidden="true">`,
		escapedFieldName,
	)

	return reFormTag.ReplaceAllStringFunc(html, func(match string) string {
		methodMatches := reMethodAttr.FindStringSubmatch(match)

		method := "GET"
		if len(methodMatches) >= 2 && methodMatches[1] != "" {
			method = strings.ToUpper(methodMatches[1])
		}

		if method == "GET" {
			return match
		}

		return match + hpInput
	})
}

