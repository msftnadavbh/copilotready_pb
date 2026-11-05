package migration

import (
	"bytes"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

// Credential checks for text that is copied as written: skills, instructions, rules, agents,
// prompts and memory. A credential blocks the item; documentation and code that only name one
// (a placeholder, an environment reference, an expression) do not.

// tokenSecret matches credential formats that are secrets wherever they appear.
var tokenSecret = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{12,}|[sr]k_live_[A-Za-z0-9]{16,}|gh[pousr]_[A-Za-z0-9_]{12,}|github_pat_[A-Za-z0-9_]{12,}|glpat-[A-Za-z0-9_-]{20,}|hf_[A-Za-z0-9]{30,}|SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{30,}|(?:AKIA|ASIA)[0-9A-Z]{16}|xox[abposr]-[A-Za-z0-9-]{10,}|npm_[A-Za-z0-9]{36}|AIza[0-9A-Za-z_-]{35}|eyJ[A-Za-z0-9_-]{8,}\.eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})|https://hooks\.slack\.com/services/T[A-Z0-9]+/B[A-Z0-9]+/[A-Za-z0-9]{16,}|-----BEGIN (?:RSA |EC |OPENSSH |DSA |ENCRYPTED |PGP )?PRIVATE KEY(?: BLOCK)?-----`)

// privateKeyEnd ends a PEM or PGP private key that tokenSecret found the start of.
var privateKeyEnd = regexp.MustCompile(`-----END (?:RSA |EC |OPENSSH |DSA |ENCRYPTED |PGP )?PRIVATE KEY(?: BLOCK)?-----`)

// keyedSecret captures a credential-like key and its value.
//
// The key is any name ending in a credential word: password, PGPASSWORD, dbPassword, DB_PASS,
// MYSQL_PWD, client_secret, apiToken, AccountKey, Ocp-Apim-Subscription-Key (group 1), with its
// separator (2: :, =, := or =>, and == so a comparison can be told apart), or a command-line flag
// (3) and how its value follows (4: = or a space).
//
// The value is an escaped-JSON string (5), a triple-quoted string (6, 7), a double- or
// single-quoted string (8, 9) with an optional b"", f"" or r"" prefix, the line break after a YAML
// block indicator (10; blockValue reads the block), an unquoted word with any HTTP auth scheme or
// template slot (11), or the line break after a bare key: (12), for a value on the next line.
var keyedSecret = regexp.MustCompile(`(?i)(?:\\?["']?\b([a-z0-9_.-]*?(?:password|passwd|passphrase|pwd|secret|token|authorization|api[_-]?key|apikey|(?:access|account|secret|private|encryption|signing|subscription|master|shared[_-]?access)[_-]?key|secret[_-]?key[_-]?base)|(?:[a-z0-9_.-]*[_.-])?pass)\\?["']?[ \t]*(:=|=>|==|[:=])[ \t]*|--((?:[a-z]+-)*(?:token|api-?key|password|passwd|pass|secret|authorization))(=|[ \t]+))(?:\\"((?:[^"\\\n]|\\[^"\n])*)\\"|[bfru]{0,2}"""([\s\S]{0,1000}?)"""|[bfru]{0,2}'''([\s\S]{0,1000}?)'''|[bfru]{0,2}"((?:[^"\\\n]|\\.)*)"|[bfru]{0,2}'((?:[^'\\\n]|\\.)*)'|[|>](?:[1-9][-+]?|[-+][1-9]?)?[ \t]*(?:#[^\r\n]*)?(\r?\n)|((?:bearer|basic|token)[ \t]+(?:<[^<>\n]+>|[^\s"';]+)|<[^<>\n]+>|[^\s"';]+)|(?:#[^\r\n]*)?(\r?\n))`)

// blockHeader is a YAML block indicator with its optional indentation indicator and comment.
var blockHeader = regexp.MustCompile(`[|>](?:([1-9])[-+]?|[-+]([1-9])?)?[ \t]*(?:#[^\r\n]*)?$`)

// urlSecret captures the user (1), password (2) and host (3) of a URL such as
// postgres://user:password@host, or redis://:password@host with no user name. A host may be an IPv6
// address in brackets: [::1].
var urlSecret = regexp.MustCompile(`\b[a-zA-Z][a-zA-Z0-9+.-]*://([^\s/:@'"]*):([^\s/@'"]+)@(\[[^\]\s]*\]|[^\s/:?#'"]*)`)

// secretFinding is one credential: where it is, its line, and what holds it (a key name, or the kind
// of token). The value itself is never kept.
type secretFinding struct {
	start, end, line int
	what             string
}

// literalSecret reports content holding a real-looking credential.
func literalSecret(content []byte) bool {
	return len(secretFindings(content)) > 0
}

// secretWhere names where content holds credentials, for a message: " (line 3: PGPASSWORD)", or ""
// when it holds none. It never includes a value.
func secretWhere(content []byte) string {
	found := secretFindings(content)
	if len(found) == 0 {
		return ""
	}
	where := fmt.Sprintf(" (line %d: %s", found[0].line, found[0].what)
	if more := len(found) - 1; more > 0 {
		where += fmt.Sprintf(", and %d more", more)
	}
	return where + ")"
}

// redactSecrets replaces each credential literalSecret finds.
func redactSecrets(content string) string {
	var out strings.Builder
	last := 0
	for _, f := range secretFindings([]byte(content)) {
		out.WriteString(content[last:f.start])
		out.WriteString("[credential-like value omitted]")
		last = f.end
	}
	return out.String() + content[last:]
}

// secretSpans returns the byte ranges of credentials in content, in order.
func secretSpans(content []byte) [][2]int {
	var spans [][2]int
	for _, f := range secretFindings(content) {
		spans = append(spans, [2]int{f.start, f.end})
	}
	return spans
}

// secretFindings returns the credentials in content, in order.
func secretFindings(content []byte) []secretFinding {
	var found []secretFinding
	add := func(start, end int, what string) {
		found = append(found, secretFinding{start: start, end: end, what: what})
	}
	for _, m := range tokenSecret.FindAllIndex(content, -1) {
		token := string(content[m[0]:m[1]])
		if strings.HasPrefix(token, "-----BEGIN") {
			// A private key is hidden to its END line, or to the end of a truncated one.
			end := len(content)
			if e := privateKeyEnd.FindIndex(content[m[1]:]); e != nil {
				end = m[1] + e[1]
			}
			add(m[0], end, "a private key")
		} else if !exampleToken(token) && !bytes.HasPrefix(content[m[1]:], []byte("...")) && !bytes.HasPrefix(content[m[1]:], []byte("…")) {
			add(m[0], m[1], tokenKind(token)) // sk-ant-api03-Zx9..., shortened in documentation, isn't one
		}
	}
	// A match ends where its value does, or at the line break after a YAML block indicator or a bare
	// key, so each search resumes there and every key is checked.
	for pos := 0; pos < len(content); {
		m := keyedSecret.FindSubmatchIndex(content[pos:])
		if m == nil {
			break
		}
		for i := range m {
			if m[i] >= 0 {
				m[i] += pos
			}
		}
		pos = m[1]
		key := group(content, m, 1) + group(content, m, 3)
		sep := group(content, m, 2) + strings.TrimSpace(group(content, m, 4))
		if sep == "==" {
			continue // a comparison: password == confirm_password
		}
		g := 5
		for g < 12 && m[2*g] < 0 {
			g++
		}
		switch {
		case g == 10 || g == 12:
			// A YAML value on the following lines, under a key that starts its line or list item.
			window, whole := lineBefore(content, m[0])
			if before := strings.TrimSpace(window); sep != ":" || !whole || before != "" && before != "-" {
				continue
			}
			keyColumn, declared := len(window), 0
			if h := blockHeader.FindSubmatch(content[m[0]:m[2*g]]); g == 10 && h != nil {
				if digit := string(h[1]) + string(h[2]); digit != "" {
					declared = keyColumn + int(digit[0]-'0') // |2: two columns past the key
				}
			}
			value, end := blockValue(content, m[2*g], keyColumn, declared)
			lines := strings.Split(value, "\n")
			if g == 12 {
				// A quoted value may hold spaces: "correct horse battery staple". Otherwise a value on the
				// next line is one word, not a nested mapping, a list or prose under a heading.
				if q := quotedScalar.FindStringSubmatch(value); len(lines) == 1 && q != nil {
					lines[0] = q[1] + q[2]
				} else if len(lines) != 1 || strings.ContainsAny(value, " \t") || nestedKey.MatchString(value) {
					continue
				}
			}
			pos = max(pos, end)
			// Each line is judged, so a placeholder line can't excuse a password beside it.
			for _, line := range lines {
				if credential(key, line, true) {
					add(m[0], end, key)
					break
				}
			}
		case g <= 9:
			// An f-string with a replacement field, f"{provider}_{region}", is built at run time.
			opening := string(content[max(m[5], m[9]):m[2*g]])
			if strings.ContainsAny(opening, "fF") && replacementField.MatchString(group(content, m, g)) {
				continue
			}
			// A triple-quoted value can span lines; each is judged, as in a YAML block.
			for _, value := range strings.Split(strings.Trim(group(content, m, g), "\r\n"), "\n") {
				if value = strings.TrimSpace(value); !laravelRules(value) && credential(key, value, true) {
					add(m[0], m[1], key)
					break
				}
			}
		default:
			value, end := group(content, m, 11), m[1]
			if m[2] > 0 && strings.ContainsRune("?&", rune(content[m[2]-1])) {
				// In a query string, ?token=${TOKEN}&password=…, & ends the value and the next key follows.
				if i := strings.IndexByte(value, '&'); i >= 0 {
					value, end = value[:i], m[22]+i
					pos = end
				}
			}
			if yamlAnchor.MatchString(value) {
				// password: &dbpass Zx9…: the anchor names the value that follows it.
				after := lineAfter(content, end)
				if a := anchoredValue.FindStringSubmatchIndex(after); a != nil {
					quoted := a[2] >= 0 || a[4] >= 0
					v := after[max(a[2], a[4], a[6]):max(a[3], a[5], a[7])]
					end += a[1]
					pos = max(pos, end)
					if credential(key, v, quoted) {
						add(m[0], end, key)
					}
				}
				continue
			}
			if !keyedCode(content, m, key, sep, value, end) && credential(key, value, false) {
				add(m[0], end, key)
			}
		}
	}
	for _, m := range urlSecret.FindAllSubmatchIndex(content, -1) {
		user, password, host := group(content, m, 1), group(content, m, 2), strings.ToLower(group(content, m, 3))
		// On its own, user:password@ or user:pass@ shows the shape. postgres:postgres@ and
		// guest:guest@ are well-known defaults, and a local address is a development setup.
		if strings.EqualFold(user, password) || placeholderWordsOnly(valueWords(password)) || host == "localhost" || host == "127.0.0.1" || host == "[::1]" || host == "0.0.0.0" {
			continue
		}
		if credential("password", password, true) {
			add(m[0], m[1], "a password in a URL")
		}
	}
	slices.SortFunc(found, func(x, y secretFinding) int { return x.start - y.start })
	// One credential can be found twice (token: ghp_…, as a key and as a token): overlapping findings
	// merge, to the further end.
	var merged []secretFinding
	for _, f := range found {
		if n := len(merged); n > 0 && f.start < merged[n-1].end {
			merged[n-1].end = max(merged[n-1].end, f.end)
			continue
		}
		merged = append(merged, f)
	}
	// Line numbers in one pass over the text, in order.
	line, counted := 1, 0
	for i := range merged {
		line += bytes.Count(content[counted:merged[i].start], []byte("\n"))
		counted, merged[i].line = merged[i].start, line
	}
	return merged
}

// lineBefore returns the text of the line before position at, up to lineContext bytes, and whether
// that is the whole of it. Each key looks at a bounded stretch, so a long line with many keys, a
// connection string, is still read in linear time.
func lineBefore(content []byte, at int) (string, bool) {
	from := max(0, at-lineContext)
	if i := bytes.LastIndexByte(content[from:at], '\n'); i >= 0 {
		return string(content[from+i+1 : at]), true
	}
	return string(content[from:at]), from == 0
}

// lineAfter returns the text of the line after position at, up to lineContext bytes.
func lineAfter(content []byte, at int) string {
	to := min(len(content), at+lineContext)
	if i := bytes.IndexByte(content[at:to], '\n'); i >= 0 {
		to = at + i
	}
	return strings.TrimRight(string(content[at:to]), "\r")
}

const lineContext = 256

var (
	// yamlAnchor names a YAML value for later reuse; anchoredValue is the value after it.
	yamlAnchor    = regexp.MustCompile(`^&[\w-]+$`)
	anchoredValue = regexp.MustCompile(`^[ \t]+(?:"([^"]*)"|'([^']*)'|([^\s#]+))`)
	// replacementField is a Python f-string field, {name}, but not an escaped {{.
	replacementField = regexp.MustCompile(`(?:^|[^{])\{[^{}]+\}`)
	numericLiteral   = regexp.MustCompile(`^[0-9][0-9._-]*$`)
	// variableName is how code names a variable, somePassword or db_pass, and a password rarely is.
	variableName = regexp.MustCompile(`^[a-z_$][A-Za-z0-9$]*[A-Z_][A-Za-z0-9_$]*$`)
)

// quotedScalar is a YAML value in quotes.
var quotedScalar = regexp.MustCompile(`^"([^"]*)"$|^'([^']*)'$`)

// tokenKind names a token format for a message.
func tokenKind(token string) string {
	for _, k := range []struct{ prefix, name string }{{"sk_live_", "a Stripe key"}, {"rk_live_", "a Stripe key"}, {"sk-", "an API key"}, {"github_pat_", "a GitHub token"}, {"gh", "a GitHub token"}, {"glpat-", "a GitLab token"}, {"hf_", "a Hugging Face token"}, {"SG.", "a SendGrid key"}, {"AKIA", "an AWS key"}, {"ASIA", "an AWS key"}, {"xox", "a Slack token"}, {"https://hooks.slack.com", "a Slack webhook"}, {"npm_", "an npm token"}, {"AIza", "a Google API key"}, {"eyJ", "a JWT"}} {
		if strings.HasPrefix(token, k.prefix) {
			return k.name
		}
	}
	return "a token"
}

// nestedKey is a YAML line that starts a mapping entry or list item rather than holding a value.
var nestedKey = regexp.MustCompile(`^(?:- |-$|[\w"'.-]+[ \t]*:(?:[ \t]|$))`)

// laravelRules reports a validation rule string such as 'required|min:8', which is not a password.
func laravelRules(v string) bool {
	if !strings.ContainsAny(v, "|:") {
		return false
	}
	for _, part := range strings.Split(v, "|") {
		if name, _, _ := strings.Cut(part, ":"); !validationRules[name] {
			return false
		}
	}
	return true
}

var validationRules = map[string]bool{"required": true, "nullable": true, "sometimes": true, "string": true, "confirmed": true, "min": true, "max": true, "between": true, "size": true, "regex": true, "different": true, "same": true, "present": true, "filled": true, "current_password": true, "password": true, "alpha_num": true, "alpha_dash": true, "bail": true}

// keyedCode reports an unquoted value that is code naming a credential rather than holding one,
// judged from the line around it: an argument or object field (login(password=pw),
// { password: pw }), a variable ($password = Read-Host, password := getPassword()), an expression
// that continues (token = await fetch(…)), the key's own name (password=password,
// --password PASSWORD), an environment variable's name (api_key: OPENAI_API_KEY), or prose
// ("Password: The user's password", "Use --password to supply it").
func keyedCode(content []byte, m []int, key, sep, value string, end int) bool {
	window, whole := lineBefore(content, m[0])
	before := strings.TrimRight(window, " \t")
	after := strings.TrimLeft(lineAfter(content, end), " \t")
	if numericLiteral.MatchString(value) {
		return false // $password = 111111; and password := 111111 hold the number
	}
	core := strings.TrimRight(value, ")]},")
	identifier := bareIdentifier.MatchString(core)
	// Code puts spaces around =; shell and .env assignments can't.
	spaced := m[4] > 0 && (content[m[4]-1] == ' ' || content[m[4]-1] == '\t')
	// In a YAML flow mapping, db: {password: admin}, a bare word is a value, not a variable.
	flow := whole && yamlFlow.MatchString(before)
	// Inside an argument list or object, a delimiter after the word ends a code expression. In
	// block YAML, password: Abc9,Def8 or Abc9}, it is part of the value.
	opener := strings.ContainsAny(before, "({[")
	trail := strings.TrimSpace(after)
	closes := trail != "" && strings.ContainsRune(")}]", rune(trail[0]))
	switch {
	case strings.HasSuffix(before, ";"):
		// A connection-string field, Server=db;Password = Zx9…;, holds a value as written.
		return sameName(key, core) || envVarName.MatchString(core)
	case sep == "=>" || sep == ":=":
		return true // :password => params[:password], token => …, password := getPassword()
	case strings.HasSuffix(before, "$") || strings.HasSuffix(before, "->"):
		return true // $password = Read-Host
	case identifier && !flow && (core != value || strings.HasPrefix(trail, ",")) && (opener || variableName.MatchString(core)):
		return true // login(password=password), { password: pw }, a field password: somePassword,
	case identifier && !flow && closes:
		return true
	case identifier && !flow && (strings.HasSuffix(before, "(") || strings.HasSuffix(before, ",") || strings.HasSuffix(before, "{")):
		return true
	case identifier && spaced && sep == "=" && strings.HasPrefix(after, ";"):
		return true // const password = pw;
	case spaced && sep == "=" && after != "" && (unicode.IsLetter(rune(after[0])) || strings.ContainsRune("(._$", rune(after[0]))):
		return true // token = await fetch(…)
	case sameName(key, core) || envVarName.MatchString(core):
		return true
	}
	if m[6] >= 0 {
		// A flag: --password admin holds a value, but in --password is optional it is prose.
		return strings.TrimSpace(group(content, m, 4)) == "" && proseWords[core]
	}
	// "Password: The user's password" is a sentence, where a YAML key would be lowercase.
	first := []rune(key)[0]
	return sep == ":" && unicode.IsUpper(first) && strings.ToUpper(key) != key && after != "" && unicode.IsLetter([]rune(after)[0])
}

var (
	bareIdentifier = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`)
	envVarName     = regexp.MustCompile(`^[A-Z][A-Z0-9]*_[A-Z0-9_]+$`)
	// yamlFlow is a line that opens a YAML flow mapping before the key: db: {, - {, or one with
	// earlier entries, db: {user: app, .
	yamlFlow = regexp.MustCompile(`^[ \t]*(?:-[ \t]+)?(?:[\w"'.-]+[ \t]*:[ \t]*)?\{(?:[ \t]*[\w"'.-]+[ \t]*:[ \t]*[^,{}()=;]*,)*[ \t]*$`)
)

// proseWords are the words of a sentence. After a flag they make it prose (--password is
// optional), and a quoted value with several of them is a description, not a passphrase.
var proseWords = map[string]bool{"the": true, "a": true, "an": true, "to": true, "is": true, "are": true, "was": true, "were": true, "be": true, "of": true, "and": true, "or": true, "for": true, "in": true, "on": true, "with": true, "it": true, "this": true, "that": true, "not": true, "no": true, "nothing": true, "do": true, "does": true, "you": true, "we": true, "they": true, "if": true, "then": true, "when": true, "from": true, "by": true, "as": true, "at": true, "can": true, "will": true, "should": true, "must": true, "may": true, "flag": true, "option": true, "takes": true, "sets": true, "only": true, "instead": true}

// sameName reports a value that repeats its key's name or its credential word, as code passing a
// variable does: password=password, --password PASSWORD, apiKey: apiKey, dbPassword=password.
func sameName(key, value string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(s))
	}
	k, v := norm(key), norm(value)
	return v != "" && (k == v || credentialWords[v] && (strings.HasSuffix(k, v) || strings.HasPrefix(v, k)))
}

var credentialWords = map[string]bool{"password": true, "passwd": true, "pwd": true, "pw": true, "pass": true, "secret": true, "token": true, "apikey": true, "key": true}

// blockValue reads the YAML block value that starts at the line break at start, under a key at
// column keyColumn. The block is its lines indented further than the key, at least as far as its
// first or as declared (|2), with blank lines inside it. It returns the value and where its last
// line ends.
func blockValue(content []byte, start, keyColumn, declared int) (string, int) {
	var value []string
	indent, end := -1, start
	if declared > 0 {
		indent = declared
	}
	for pos := start + bytes.IndexByte(content[start:], '\n'); pos < len(content); {
		pos++ // past the line break
		next := bytes.IndexByte(content[pos:], '\n')
		if next < 0 {
			next = len(content) - pos
		}
		line := strings.TrimRight(string(content[pos:pos+next]), "\r")
		if strings.TrimSpace(line) != "" {
			n := len(line) - len(strings.TrimLeft(line, " \t"))
			if indent < 0 {
				if n <= keyColumn {
					break
				}
				indent = n
			}
			if n < indent {
				break
			}
			value = append(value, strings.TrimSpace(line))
			end = pos + len(line)
		}
		pos += next
	}
	return strings.Join(value, "\n"), end
}

func group(content []byte, m []int, g int) string {
	if m[2*g] < 0 {
		return ""
	}
	return string(content[m[2*g]:m[2*g+1]])
}

// exampleToken reports a token-shaped placeholder: AWS's documented keys such as
// AKIAIOSFODNN7EXAMPLE, a masked token such as xoxb-xxxxxxxxxx or sk-proj-xxxxxxxx, or one made of
// placeholder words such as sk-your-key-here or ghp_EXAMPLE_TOKEN. A token that only has xxxx or
// example somewhere inside a word is not one.
func exampleToken(token string) bool {
	if (strings.HasPrefix(token, "AKIA") || strings.HasPrefix(token, "ASIA")) && strings.HasSuffix(token, "EXAMPLE") {
		return true
	}
	rest := token
	if i := strings.IndexAny(token, "-_"); i >= 0 && i < 12 {
		rest = token[i+1:]
	}
	if strings.Trim(rest, "xX*-_") == "" {
		return true
	}
	for _, segment := range strings.FieldsFunc(rest, func(r rune) bool { return r == '-' || r == '_' }) {
		if len(segment) >= 8 && strings.Trim(segment, "xX*") == "" {
			return true // sk-proj-xxxxxxxxxxxxxxxx
		}
	}
	words := valueWords(rest)
	for _, word := range words {
		if markerWords[word] {
			return true
		}
	}
	return len(words) > 1 && placeholderWordsOnly(words)
}

// Shapes that are not literal credentials:
//   - an environment, command or argument reference: $VAR, ${VAR}, %VAR%, $(command), $1, $@,
//     process.env.X, os.environ[…], getenv(…), LiteLLM's os.environ/X;
//   - a YAML tag (!secret db_password, !Ref DBPassword), a format verb (%s) or a bind parameter
//     (:password, @password);
//   - a template slot: <token>, {{token}}, {token}, [API_KEY];
//   - a file path: ~/.config/gh/token, ./secrets/db.
//
// Unquoted, an expression is code naming a credential, not one: a call, an index, an object
// literal or a dotted name (z.string().min(8), Optional[str], options.apiKey). After a password key
// a dotted name must look like code (req.body.password, settings.db_password), since hunter.horse
// is a password. After a key other than a password, a short lowercase name of up to three words
// (my-secret-name, oauth2) names a credential rather than holding one. Quoted, the same words may
// be a passphrase (correct-horse-staple).
//
// concatenation is code between two string literals: '?token=' + encode(TOKEN) + '&v=1' leaves
// " + encode(TOKEN) + " between quotes, where a value would be.
var (
	referenceValue = regexp.MustCompile(`^(?:\$\{?[A-Za-z_][A-Za-z0-9_]*\}?|\$[0-9@*#?]|\$(?:\(|env:|\{).*|(?:secretref|keyvaultref):.*|%[A-Za-z_][A-Za-z0-9_]*%|process\.env\.[A-Za-z_][A-Za-z0-9_]*|os\.environ(?:\[|\.get\(|/).*|(?:os\.)?getenv\(.*|![A-Za-z][\w:]*|%(?:\(\w+\))?[sdvq]|[:@][A-Za-z_]\w*)$`)
	slotValue      = regexp.MustCompile(`^(?:<[^<>]+>|\{\{[^{}]+\}\}|\{[A-Za-z_][\w .-]*\}|\[[A-Z][A-Z0-9_ ]*\])$`)
	pathValue      = regexp.MustCompile(`^(?:~|\.{1,2})?/[\w.~/-]*$|^[A-Za-z]:\\`)
	callValue      = regexp.MustCompile(`^[A-Za-z_$][\w$.]*[(\[]`)
	dottedName     = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*(?:\.[A-Za-z_$][A-Za-z0-9_$]*)+$`)
	codeDotted     = regexp.MustCompile(`^(?:self|this|options|opts|config|cfg|settings|conf|process|os|env|args|argv|req|request|params|props|ctx|context|data|body|form|input|user|credentials|creds|secrets|vars)\.|_|[a-z][A-Z]`)
	typeName       = regexp.MustCompile(`^[A-Z][a-z]+(?:-?[A-Z][a-z]+)*$`)
	nameValue      = regexp.MustCompile(`^[A-Za-z][a-z]{0,14}[0-9]{0,4}(?:[-_][A-Za-z][a-z]{0,14}[0-9]{0,4}){0,2}$`)
	typeSuffix     = regexp.MustCompile(`(?:Str|String|Bytes|Text|Field|Type|Input|Number|Int|Integer|Bool|Boolean|Object|Any)$`)
	concatenation  = regexp.MustCompile(`^\s*\+\s*[\w$.]+\s*[-+,()\[\]{}]|^\s+\+|\+\s+$`)
)

var authScheme = map[string]bool{"bearer": true, "basic": true, "token": true}

// placeholderWords make up placeholder values such as your_api_key_here, optionally followed by
// digits (abc123) or next to a short number (token_1).
var placeholderWords = map[string]bool{"your": true, "my": true, "the": true, "a": true, "an": true, "api": true, "key": true, "apikey": true, "access": true, "auth": true, "token": true, "secret": true, "password": true, "pass": true, "client": true, "here": true, "value": true, "example": true, "sample": true, "dummy": true, "fake": true, "test": true, "demo": true, "placeholder": true, "changeme": true, "change": true, "me": true, "replace": true, "with": true, "insert": true, "redacted": true, "xxx": true, "abc": true, "foo": true, "bar": true, "name": true, "user": true, "username": true, "pwd": true, "passwd": true, "mock": true, "stub": true, "bearer": true, "basic": true, "string": true, "required": true, "optional": true, "none": true, "null": true, "undefined": true, "true": true, "false": true}

// markerWords make a whole value a placeholder wherever they appear in it: xi-api-key-here.
var markerWords = map[string]bool{"your": true, "here": true, "placeholder": true, "changeme": true, "replace": true, "insert": true, "example": true, "redacted": true, "dummy": true, "fake": true, "sample": true}

// standaloneWords are placeholders on their own even after a password key, where a single word
// such as admin or password123 is a weak but real password.
var standaloneWords = map[string]bool{"string": true, "str": true, "text": true, "bytes": true, "number": true, "int": true, "bool": true, "any": true, "object": true, "none": true, "null": true, "nil": true, "undefined": true, "required": true, "optional": true, "true": true, "false": true, "changeme": true, "placeholder": true, "redacted": true, "xxx": true, "example": true, "sample": true, "dummy": true, "fake": true, "value": true}

// valueWords splits a value into lowercase words at separators and camelCase humps:
// YourApiKeyHere is your, api, key, here.
func valueWords(v string) []string {
	var b strings.Builder
	runes := []rune(v)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) && unicode.IsLower(runes[i-1]) {
			b.WriteRune(' ')
		}
		b.WriteRune(r)
	}
	return strings.FieldsFunc(strings.ToLower(b.String()), func(r rune) bool { return r == '_' || r == '-' || r == '.' || unicode.IsSpace(r) })
}

// placeholderWordsOnly reports words that are all placeholder words, allowing a short number
// beside them but not a number alone.
func placeholderWordsOnly(words []string) bool {
	named := false
	for _, word := range words {
		switch {
		case placeholderWords[strings.TrimRight(word, "0123456789")]:
			named = true
		case strings.Trim(word, "0123456789") != "" || len(word) > 4:
			return false
		}
	}
	return named
}

// masked reports a value made only of masking characters: ********, xxxxxx or xxxx-xxxx-xxxx, but
// not 111111 or aaaaaa, which are weak passwords.
func masked(v string) bool {
	return strings.Trim(v, "*xX•#.-_") == "" && strings.ContainsAny(v, "*xX•#")
}

// sentence reports prose: five or more words that make two sentences, or one with at least two of
// the small words sentences are made of. A passphrase such as "correct horse battery staple
// tonight!" is neither.
func sentence(v string) bool {
	v = strings.TrimSpace(v)
	fields := strings.Fields(v)
	ends := len(v) > 0 && strings.ContainsAny(v[len(v)-1:], ".!?")
	if len(fields) < 5 || !ends && !strings.Contains(v, ". ") {
		return false
	}
	if ends && strings.Contains(strings.TrimRight(v, ".!?"), ". ") {
		return true
	}
	small := 0
	for _, field := range fields {
		if proseWords[strings.ToLower(strings.Trim(field, `.,;:!?"'`))] {
			small++
		}
	}
	return small >= 2
}

// credential judges the value after a credential-like key. A quoted value is taken as written; an
// unquoted one may be code.
func credential(key, value string, quoted bool) bool {
	lower := strings.ToLower(key)
	passwordKey := strings.Contains(lower, "pass") || strings.Contains(lower, "pwd")
	if quoted && concatenation.MatchString(value) {
		return false // code between two string literals, not a value
	}
	v := strings.TrimSpace(value)
	if fields := strings.Fields(v); len(fields) > 1 && authScheme[strings.ToLower(fields[0])] {
		v = strings.TrimSpace(v[len(fields[0]):]) // "Bearer <token>": judge the token
	}
	if strings.HasSuffix(v, "...") || strings.HasSuffix(v, "…") || referenceValue.MatchString(v) {
		return false // shortened in documentation (sk-ant-...), or !secret or :password before trimming
	}
	v = strings.Trim(v, `"'.,:;!?`+"`") // quotes and sentence punctuation around a Markdown example
	if masked(v) {
		return false
	}
	if !strings.ContainsFunc(v, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
		// Unquoted, punctuation is syntax: ')' in input('Password: '). Quoted, !@#$%^&*() is a password.
		if !quoted || len(v) < 6 || strings.Trim(v, v[:1]) == "" {
			return false
		}
	}
	if referenceValue.MatchString(v) || slotValue.MatchString(v) || pathValue.MatchString(v) || strings.Contains(v, "${") {
		return false // also a value built at run time: `run-${Date.now()}`
	}
	if strings.HasSuffix(v, "EXAMPLE") || strings.HasSuffix(v, "EXAMPLEKEY") {
		return false // AWS's documented keys: wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY
	}
	if tokenSecret.FindString(v) == v && exampleToken(v) {
		return false // a masked or documented token: ghp_xxxxxxxx
	}
	if strings.Contains(v, "://") {
		return false // an address; a password inside one is urlSecret's
	}
	if !quoted {
		// A call or index: hash(password), os.Getenv( cut at its quote, styles.find(t => …,
		// hasLength({ min: 8 }). Zx9(wLm2 is a password.
		open := strings.IndexAny(v, "([")
		call := callValue.MatchString(v) && (open == len(v)-1 || !unicode.IsLetter(rune(v[open+1])) && !unicode.IsDigit(rune(v[open+1])) || strings.ContainsAny(v, ")]") || !strings.ContainsAny(v[:open], "0123456789"))
		if call || strings.HasPrefix(v, "{") || strings.HasPrefix(v, "[") || dottedName.MatchString(v) && (!passwordKey || codeDotted.MatchString(v)) {
			return false
		}
		if typeName.MatchString(v) && (!passwordKey || typeSuffix.MatchString(v)) {
			return false // a type (SecretStr, Read-Host); after a password key, Qwerty is a password
		}
	}
	if sentence(value) {
		return false // a description after PASS:, not a passphrase
	}
	words := valueWords(v)
	for _, word := range words {
		if markerWords[word] {
			return false
		}
	}
	if placeholderWordsOnly(words) {
		return passwordKey && len(words) == 1 && !standaloneWords[words[0]]
	}
	if passwordKey {
		return len(words) != 1 || !standaloneWords[words[0]]
	}
	return len(v) >= 6 && (quoted || !nameValue.MatchString(v))
}
