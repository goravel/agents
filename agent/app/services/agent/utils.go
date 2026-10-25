package agent

import (
	"strconv"
	"strings"
)

// parseSenders splits a comma-separated allowlist into lowercased sender keys,
// ignoring blank entries.
func parseSenders(raw string) map[string]struct{} {
	senders := make(map[string]struct{})
	for _, part := range strings.Split(raw, ",") {
		if part = strings.ToLower(strings.TrimSpace(part)); part != "" {
			senders[part] = struct{}{}
		}
	}

	return senders
}

// normalizeMention turns the configured handle into a mention: it trims
// surrounding space, ensures the leading `@` and falls back to DefaultMention
// when the value is blank.
func normalizeMention(raw string) string {
	mention := strings.TrimSpace(raw)
	if mention == "" {
		return DefaultMention
	}
	if !strings.HasPrefix(mention, "@") {
		mention = "@" + mention
	}

	return mention
}

// retryKey identifies an event for retry accounting, preferring the relay's
// delivery id and falling back to the event id when it is absent.
func retryKey(ev *event) string {
	if ev.DeliveryID != "" {
		return ev.DeliveryID
	}

	return strconv.FormatUint(ev.ID, 10)
}

const (
	// shellSafeRunes are the characters an argument may contain without being
	// quoted: letters, digits and the punctuation that is inert in argv. Tilde,
	// glob characters, `$`, backslash, quotes and whitespace stay out so the
	// shell never expands a logged argument. `=` stays in because it is inert
	// mid-token (`k=v`, `--model a=b`); quoteArg quotes the leading `=` itself.
	shellSafeRunes = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"0123456789@%+=:,./-_"
)

// needsShellQuote reports whether r forces its argument into quotes.
func needsShellQuote(r rune) bool {
	return !strings.ContainsRune(shellSafeRunes, r)
}

// quoteArg renders one argument as a shell word. An argument made only of
// shellSafeRunes re-splits to itself, so it is returned bare; anything else —
// including the empty string, a space, a non-ASCII rune or a leading `=` — is
// wrapped in single quotes, where the shell expands nothing. The leading `=` is
// quoted even though POSIX sh leaves it alone, because zsh rewrites a bare word
// starting with `=` to the path of the command named after it. An embedded
// single quote closes the quote, escapes itself as `\'` and reopens it.
// Newlines are left inside the quotes, so a multi-line prompt survives a
// copy-paste.
func quoteArg(arg string) string {
	if arg != "" && arg[0] != '=' && !strings.ContainsFunc(arg, needsShellQuote) {
		return arg
	}

	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// shellCommand renders argv as a command line a POSIX shell splits back into
// exactly the same arguments. The dispatcher logs the paseo run this way so an
// operator can paste it into a terminal: space-joining the argv instead would
// re-split a multi-word title or a multi-line prompt, and paseo would take the
// title's first word as the title and treat the rest as the prompt positional.
func shellCommand(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = quoteArg(arg)
	}

	return strings.Join(quoted, " ")
}
