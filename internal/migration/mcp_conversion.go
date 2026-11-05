package migration

import (
	"net/url"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var envTemplate = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
var credentialFlag = regexp.MustCompile(`^--?(?:token|api[_-]?key|password|secret|authorization)(?:=|$)`)

func isEnvRef(value string) bool {
	return envTemplate.FindString(value) == value && value != ""
}

// Claude suppresses its own and provider credentials in remote templates. Do
// not turn those suppressed references into credentials sent by another client.
func protectedClaudeReference(value string) bool {
	for _, match := range envTemplate.FindAllStringSubmatch(value, -1) {
		name := strings.ToUpper(match[1])
		if strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_") || strings.HasPrefix(name, "AWS_") || strings.HasPrefix(name, "AZURE_") || strings.HasPrefix(name, "GOOGLE_") || strings.HasPrefix(name, "GCP_") || name == "NPM_TOKEN" || strings.HasSuffix(name, "_PROXY") {
			return true
		}
	}
	return false
}

func convertMCP(source, name string, cfg map[string]any) (map[string]any, []string) {
	out := map[string]any{}
	var reasons []string
	if !namePattern.MatchString(name) {
		reasons = append(reasons, "Server name needs a safe target name.")
	}
	if cfg == nil {
		return out, []string{"Expected a server object."}
	}
	if value, present := cfg["type"]; present {
		if _, ok := value.(string); !ok {
			reasons = append(reasons, "Transport type must be a string.")
		}
	}
	known := map[string]bool{"type": true, "command": true, "args": true, "cwd": true, "env": true, "url": true, "headers": true, "tools": true, "timeout": true, "disabled": true, "enabled": true, "description": true}
	if source == "codex" {
		for _, k := range []string{"env_vars", "bearer_token_env_var", "http_headers", "env_http_headers", "auth"} {
			known[k] = true
		}
	}
	for _, k := range sortedKeys(cfg) {
		if !known[k] {
			reasons = append(reasons, "Source field needs manual adaptation: "+k+".")
		}
	}
	for _, key := range []string{"disabled", "enabled"} {
		if value, exists := cfg[key]; exists {
			flag, ok := value.(bool)
			if !ok {
				reasons = append(reasons, key+" must be a boolean.")
			} else if key == "disabled" && flag || key == "enabled" && !flag {
				reasons = append(reasons, "This server is disabled in the source. It will not be enabled by migration.")
			}
		}
	}
	if source == "codex" {
		if auth, ok := cfg["auth"]; ok && auth != "oauth" {
			reasons = append(reasons, "Source account authentication is not portable. Set up a separate Copilot sign-in.")
		}
	}
	if _, ok := cfg["tools"]; ok {
		reasons = append(reasons, "Source tool permissions require review; they are not copied or widened.")
	}
	if timeout, ok := cfg["timeout"]; ok {
		if source == "copilot" {
			out["timeout"] = timeout
		} else {
			reasons = append(reasons, "Source timeout behavior needs manual mapping.")
		}
	}
	endpoint, hasURL := cfg["url"]
	if hasURL {
		raw, ok := endpoint.(string)
		u, err := url.Parse(raw)
		kind := str(cfg["type"])
		if !ok || err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.Contains(raw, "$") || secretText.MatchString(raw) || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"))) {
			reasons = append(reasons, "URL needs review: use a fixed HTTP(S) endpoint without credentials, variables or parameters.")
		} else if kind != "" && kind != "http" && kind != "sse" {
			reasons = append(reasons, "Remote transport needs manual adaptation.")
		} else {
			out["url"] = raw
			out["type"] = "http"
			if kind == "sse" {
				out["type"] = "sse"
			}
		}
		for _, key := range []string{"command", "args", "cwd", "env", "env_vars"} {
			if _, ok := cfg[key]; ok {
				reasons = append(reasons, "Remote server field needs review: "+key+".")
			}
		}
	} else {
		command, ok := cfg["command"].(string)
		kind := str(cfg["type"])
		if !ok || command == "" || strings.ContainsAny(command, "\r\n$") || secretText.MatchString(command) {
			reasons = append(reasons, "A fixed executable command is required.")
		} else if kind != "" && kind != "stdio" && kind != "local" {
			reasons = append(reasons, "Local transport needs manual adaptation.")
		} else if strings.ContainsAny(command, "/\\") && !filepath.IsAbs(command) {
			reasons = append(reasons, "A relative executable path needs review in the chosen project.")
		} else if _, err := exec.LookPath(command); err != nil {
			reasons = append(reasons, "Executable dependency is not available on this computer.")
		} else {
			out["type"] = "local"
			out["command"] = command
		}
		if value, ok := cfg["args"]; ok {
			args, valid := value.([]any)
			if !valid {
				reasons = append(reasons, "Arguments must be a string array.")
			} else {
				clean := []string{}
				credentialNext := false
				for _, arg := range args {
					value, ok := arg.(string)
					if !ok {
						reasons = append(reasons, "Arguments must be strings.")
						continue
					}
					flag := credentialFlag.MatchString(strings.ToLower(value))
					if credentialNext || flag || secretText.MatchString(value) || strings.Contains(value, "$") || strings.Contains(strings.ToLower(value), "token=") || strings.Contains(strings.ToLower(value), "password=") {
						reasons = append(reasons, "Argument credentials or substitutions need manual setup.")
						credentialNext = flag && !strings.Contains(value, "=")
						continue
					}
					clean = append(clean, value)
				}
				out["args"] = clean
			}
		}
		if value, ok := cfg["cwd"]; ok {
			cwd, valid := value.(string)
			if !valid || !filepath.IsAbs(cwd) || strings.Contains(cwd, "$") {
				reasons = append(reasons, "Working directory must be an explicit absolute path.")
			} else {
				out["cwd"] = cwd
			}
		}
		for _, key := range []string{"headers", "http_headers", "env_http_headers", "bearer_token_env_var", "auth"} {
			if _, ok := cfg[key]; ok {
				reasons = append(reasons, "HTTP authentication field cannot be used by a local server: "+key+".")
			}
		}
	}
	// Keep references, not their resolved values, in all staged and active files.
	for _, key := range []string{"env", "headers", "http_headers"} {
		value, present := cfg[key]
		if !present {
			continue
		}
		values := mapValue(value)
		if values == nil {
			reasons = append(reasons, key+" must be an object.")
			continue
		}
		target := key
		if key == "http_headers" {
			target = "headers"
		}
		safe := mapValue(out[target])
		if safe == nil {
			safe = map[string]any{}
		}
		for _, k := range sortedKeys(values) {
			value, ok := values[k].(string)
			if !ok {
				reasons = append(reasons, key+" values must be strings.")
				continue
			}
			if target == "env" && !envName.MatchString(k) || target == "headers" && (!validHeader(k) || reservedHeader(k)) {
				reasons = append(reasons, "Invalid or reserved "+key+" name.")
				continue
			}
			reference := isEnvRef(value) || target == "headers" && strings.EqualFold(k, "Authorization") && strings.HasPrefix(value, "Bearer ") && isEnvRef(strings.TrimPrefix(value, "Bearer "))
			// Codex env/http_headers values are literal, not interpolation syntax.
			if source == "codex" && strings.Contains(value, "$") {
				reasons = append(reasons, "Literal Codex values containing substitutions require review.")
				continue
			}
			literalOK := safeLiteral(key, k, value)
			if !(reference || literalOK) || strings.ContainsAny(value, "\r\n") {
				reasons = append(reasons, "Literal credentials or unsupported "+key+" values require secure re-entry; they are not copied.")
				continue
			}
			if strings.HasPrefix(source, "claude") && target == "headers" && protectedClaudeReference(value) {
				reasons = append(reasons, "Claude protects this credential variable. Use a dedicated MCP credential reference after review.")
				continue
			}
			duplicate := false
			for existing := range safe {
				duplicate = duplicate || existing == k || target == "headers" && strings.EqualFold(existing, k)
			}
			if duplicate {
				reasons = append(reasons, "Duplicate header or environment mapping needs review.")
				continue
			}
			safe[k] = value
		}
		if len(safe) > 0 {
			out[target] = safe
		}
	}
	if source == "codex" {
		if value, present := cfg["env_vars"]; present {
			values, ok := value.([]any)
			if !ok {
				reasons = append(reasons, "env_vars must be an array of variable names.")
			} else {
				env := mapValue(out["env"])
				if env == nil {
					env = map[string]any{}
				}
				for _, v := range values {
					name, ok := v.(string)
					if entry := mapValue(v); entry != nil {
						name, ok = entry["name"].(string)
						if entry["source"] != nil && entry["source"] != "local" || len(entry) > 2 {
							reasons = append(reasons, "Remote or custom environment forwarding needs manual adaptation.")
							continue
						}
					}
					if !ok || !envName.MatchString(name) {
						reasons = append(reasons, "Invalid forwarded environment variable name.")
						continue
					}
					if _, exists := env[name]; exists {
						reasons = append(reasons, "An environment variable has both a literal and forwarded value.")
						continue
					}
					env[name] = "${" + name + "}"
				}
				if len(env) > 0 {
					out["env"] = env
				}
			}
		}
		headers := mapValue(out["headers"])
		if headers == nil {
			headers = map[string]any{}
		}
		addHeader := func(key, ref, prefix string) {
			if !validHeader(key) || reservedHeader(key) || !envName.MatchString(ref) {
				reasons = append(reasons, "Invalid credential/header reference.")
				return
			}
			for existing := range headers {
				if strings.EqualFold(existing, key) {
					reasons = append(reasons, "Competing authentication headers need review.")
					return
				}
			}
			headers[key] = prefix + "${" + ref + "}"
		}
		if value, present := cfg["env_http_headers"]; present {
			values := mapValue(value)
			if values == nil {
				reasons = append(reasons, "env_http_headers must map headers to variable names.")
			} else {
				for _, key := range sortedKeys(values) {
					ref, ok := values[key].(string)
					if !ok {
						reasons = append(reasons, "Header variable names must be strings.")
						continue
					}
					addHeader(key, ref, "")
				}
			}
		}
		if value, present := cfg["bearer_token_env_var"]; present {
			ref, ok := value.(string)
			if !ok {
				reasons = append(reasons, "Bearer token variable must be a name.")
			} else {
				addHeader("Authorization", ref, "Bearer ")
			}
		}
		if len(headers) > 0 {
			out["headers"] = headers
		}
	}
	return out, reasons
}

func validHeader(name string) bool {
	return regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$").MatchString(name)
}
func reservedHeader(name string) bool {
	switch strings.ToLower(name) {
	case "host", "content-length", "connection", "transfer-encoding", "mcp-session-id", "mcp-protocol-version":
		return true
	}
	return false
}
func safeLiteral(kind, key, value string) bool {
	if strings.Contains(value, "$") || secretText.MatchString(value) || strings.ContainsAny(value, "\r\n") {
		return false
	}
	if kind == "env" {
		switch strings.ToUpper(key) {
		case "NODE_ENV":
			return value == "production" || value == "development" || value == "test"
		case "LOG_LEVEL":
			return value == "debug" || value == "info" || value == "warn" || value == "error" || value == "trace" || value == "silent"
		case "DEBUG", "NO_COLOR", "FORCE_COLOR":
			return value == "" || value == "0" || value == "1" || value == "true" || value == "false"
		}
	}
	if kind == "headers" || kind == "http_headers" {
		switch strings.ToLower(key) {
		case "accept", "content-type":
			return value == "application/json" || value == "text/event-stream" || value == "application/json, text/event-stream"
		case "x-figma-region":
			return value == "us" || value == "eu"
		}
	}
	return false
}
