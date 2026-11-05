package migration

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestCodeAndPlaceholdersAreNotCredentials(t *testing.T) {
	for _, s := range []string{
		"password: z.string().min(8)",
		"apiKey: options.apiKey",
		"apiKey: import.meta.env.VITE_API_KEY",
		"password: req.body.password",
		"api_key = Column(String(255))",
		"password: Optional[str]",
		"password: str",
		"password: SecretStr",
		"api_key=args.api_key",
		"client_secret = settings.client_secret",
		"secret: my-secret-name",
		"authorization = oauth2",
		"Authorization: Bearer <access token>",
		"api_key: <paste your key>",
		"password=$(pass show foo)",
		`password="$(cat /run/secrets/x)"`,
		"password=hash(password)",
		"Authorization: Basic base64(user:pass)",
		"ELEVENLABS_API_KEY=xi-api-key-here",
		"api_key:\n  description: The key",
		"password: your_password_here",
		`password = "your password here"`,
		`api_key: "YOUR_API_KEY_1"`,
		"api_key: abc123",
		"token: string",
		"max_tokens: 4096",
		"token = response[\"token\"]",
		`headers = {"Authorization": "Bearer " + token}`,
		"api-key: $ELEVENLABS_API_KEY",
		"export OPENAI_API_KEY=sk-your-key-here",
		"aws_access_key_id = AKIAIOSFODNN7EXAMPLE",
		"SLACK_BOT_TOKEN=xoxb-xxxxxxxxxxxx",
		"postgres://user:password@localhost/db",
		"postgres://user:${DB_PASSWORD}@localhost/db",
		"password: \"\"",
		"password: ********",
		"const u = base + '?token=' + encodeURIComponent(TOKEN) + '&v=1';",
		"fetch(url + '?token=' + TOKEN, { method: 'GET' })",
		"headers: { token: 'mock-token' }",
		`secret: "my-secret-name"`,
		"api_key=None",
		`$token = "$env:TEMP\\kv-token.txt"`,
		"$token = Read-Host",
		"DB_PASSWORD=secretref:db-password",
		"--set-env-vars db-password=keyvaultref:https://vault.vault.azure.net/secrets/db",
		`"apiKey": "YourApiKeyHere"`,
		"token: https://login.example.com/oauth2/token",
		"Authorization: Bearer {token}",
		"mongodb://user:pass@localhost/db",
		"mongodb+srv://USERNAME:PASSWORD@cluster.example.net",
		"redis://default:<password>@cache:6379",
		"token: ghp_xxxxxxxxxxxxxxxxxxxx",
		"key: AKIAI44QH8DHBEXAMPLE",
		"GITHUB_TOKEN=ghp_YOUR_TOKEN_HERE",
		"const u = base+'?token='+TOKEN+'&v=1';",
		"password: |\n  your password here\nname: x",
		"config:\n  token: |\n    your token here\n  name: x\n",
		"settings:\n  password: |\n\n    changeme\n\n  user: x\n",
		"password: SecureString",
		"password: SecretStr\ntoken: Read-Host",
		"redis://:<password>@cache:6379",
		"password: |\n  your password here\n  and here\n",
		"password: ********",
		"password: xxxxxxxx",
		"password: |2 # the value\n    ••••••\n",
		"const ok = token =>\n  /^[a-z_-][\\w-]*$/i.test(token) && !reserved.has(token);",
	} {
		if literalSecret([]byte(s)) {
			t.Errorf("flagged as a credential: %q", s)
		}
	}
}

func TestLiteralCredentialsAreFound(t *testing.T) {
	for _, s := range []string{
		"password: admin",
		"password: 12345",
		"password: password123",
		"password: test123456",
		"password: 'abc12'",
		"password: |\n  Zx9wLm2Qp7Rt4Kv8",
		"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMIK7MDENGbPxRfiCYzw9q",
		"aws_access_key_id = AKIA4QWERTYUIOPASDFG",
		"SECRET_KEY=Zx9wLm2Qp7Rt4Kv8",
		"SLACK_BOT_TOKEN=xoxb-1234567890-abcdefghij",
		"//registry.npmjs.org/:_authToken=npm_abcdefghijklmnopqrstuvwxyz0123456789",
		"auth_token: Zx9wLm2Qp7Rt4Kv8",
		`token = "Zx9wLm2Qp7Rt4Kv8"`,
		"DATABASE_URL=postgres://app:Zx9wLm2Qp7@db.internal/app",
		`{\"password\": \"Zx9wLm2Qp7Rt4Kv8\"}`,
		"key=AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U",
		"api_key: deadbeefcafe1234abcd5678",
		`secret: "my production passphrase"`,
		"password: |\n  correct horse\n  Zx9wLm2Qp7Rt4Kv8",
		"postgres://app:admin@db.internal/app",
		"postgres://app:securepass2026@db.internal/app",
		"token: ghp_Ab9xxxxCd8Ef7Gh6Ij5Kl4",
		"Sign in with ghp_Ab9xxxxCd8Ef7Gh6Ij5Kl4 today",
		"Sign in with sk-proj-Ab9exampleCd8Ef7Gh6 today",
		"token: ghp_Ab9Cd8Ef7Gh6IjExample5Kl4",
		`password = "Zx9wLm2Qp7Rt4Kv8+"`,
		`password = "+Zx9wLm2Qp7Rt4Kv8"`,
		`secret: "correct-horse-staple"`,
		"password: |\n  correct horse\n\n  Zx9wLm2Qp7Rt4Kv8",
		"password: |\n  string\n\n  Zx9wLm2Qp7Rt4Kv8",
		"password: |\r\n  string\r\n\r\n  Zx9wLm2Qp7Rt4Kv8\r\n",
		"config:\n  token: |\n    your token here\n  password: Zx9wLm2Qp7Rt4Kv8\n",
		"redis://:Zx9wLm2Qp7@cache:6379/0",
		"password: Qwerty",
		"password: CorrectHorseStaple",
		`password: "111111"`,
		"password: 000000",
		"password: aaaaaa",
		"password: |\n  your password here\n  Zx9wLm2Qp7Rt4Kv8",
		"password: | # note\n  string\n  Zx9wLm2Qp7Rt4Kv8",
		"password: |2\n    Zx9wLm2Qp7Rt4Kv8",
		"password: >-2 # folded\n    correct horse\n",
		"-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAA\n-----END OPENSSH PRIVATE KEY-----",
	} {
		if !literalSecret([]byte(s)) {
			t.Errorf("credential not found: %q", s)
		}
	}
}

func TestPreviewsRedactOnlyCredentials(t *testing.T) {
	text := "Send `Authorization: Bearer <token>`.\npassword = \"Zx9wLm2Qp7Rt4Kv8\"\n"
	got := redactSecrets(text)
	if !strings.Contains(got, "Bearer <token>") || strings.Contains(got, "Zx9wLm2Qp7Rt4Kv8") || !strings.Contains(got, "[credential-like value omitted]") {
		t.Fatalf("preview: %q", got)
	}
}

func TestYAMLBlockCredentialIsRedactedWhole(t *testing.T) {
	for _, text := range []string{
		"password: |\n  correct horse\n  Zx9wLm2Qp7Rt4Kv8\nname: kept\n",
		"password: |\n  correct horse\n\n  Zx9wLm2Qp7Rt4Kv8\nname: kept\n",
		"config:\n  password: >-\n    correct horse\n\n    Zx9wLm2Qp7Rt4Kv8\nname: kept\n",
	} {
		got := redactSecrets(text)
		if strings.Contains(got, "correct horse") || strings.Contains(got, "Zx9wLm2Qp7Rt4Kv8") || !strings.Contains(got, "\nname: kept\n") {
			t.Fatalf("preview: %q", got)
		}
	}
	text := "config:\n  token: |\n    your token here\n  password: Zx9wLm2Qp7Rt4Kv8\n  name: kept\n"
	if got := redactSecrets(text); !strings.Contains(got, "your token here") || strings.Contains(got, "Zx9wLm2Qp7Rt4Kv8") || !strings.Contains(got, "name: kept") {
		t.Fatalf("preview: %q", got)
	}
	if got := redactSecrets("REDIS_URL=redis://:Zx9wLm2Qp7@cache:6379/0"); strings.Contains(got, "Zx9wLm2Qp7") {
		t.Fatalf("preview: %q", got)
	}
}

func TestPrivateKeysAndBlockHeadersAreRedactedWhole(t *testing.T) {
	for _, text := range []string{
		"Key:\n-----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEAzx9wLm2Qp7\nRt4Kv8Zx9wLm2Qp7Rt4Kv8\n-----END RSA PRIVATE KEY-----\nname: kept\n",
		"password: |2\n    MIIEowIBAAKCAQEAzx9wLm2Qp7\n\n    Rt4Kv8Zx9wLm2Qp7Rt4Kv8\nname: kept\n",
		"password: | # note\n  your password here\n  MIIEowIBAAKCAQEAzx9wLm2Qp7\n  Rt4Kv8Zx9wLm2Qp7Rt4Kv8\nname: kept\n",
	} {
		got := redactSecrets(text)
		if strings.Contains(got, "MIIEowIBAAKCAQEAzx9wLm2Qp7") || strings.Contains(got, "Rt4Kv8Zx9wLm2Qp7Rt4Kv8") || !strings.Contains(got, "name: kept") {
			t.Fatalf("preview: %q", got)
		}
	}
	if got := redactSecrets("Truncated:\n-----BEGIN PRIVATE KEY-----\nMIIEowIBAAKCAQEAzx9wLm2Qp7"); strings.Contains(got, "MIIEowIBAAKCAQEAzx9wLm2Qp7") {
		t.Fatalf("truncated key preview: %q", got)
	}
}

func TestCodeThatNamesACredentialIsNotOne(t *testing.T) {
	for _, s := range []string{
		"connect(host=h, user=u, password=password)",
		"login(username=username, password=password)",
		"db.connect(password=pw)",
		"const creds = { username: username, password: password }",
		"if (user.password == input) {",
		"if password == confirm_password:",
		`password := os.Getenv("X")`,
		"password := getPassword()",
		"const password = pw;",
		"const apiToken = await getToken();",
		"'password' => 'required|min:8',",
		"user = User.create(:password => params[:password])",
		"PASSWORD=$1",
		`password="$@"`,
		"Run it with --password PASSWORD.",
		"Use --password to supply it.",
		"--password is optional.",
		"pw = input('Password: ')",
		"pw = getpass.getpass('Password: ')",
		"const pw = prompt('Password:')",
		"password: !secret db_password",
		"MasterUserPassword: !Ref DBPassword",
		"$password = Read-Host -AsSecureString",
		`query = "user=%s password=%s"`,
		"password=%s",
		"WHERE password = :password",
		"Password: The user's password, at least 12 characters.",
		"api_key: os.environ/OPENAI_API_KEY",
		"api_key: OPENAI_API_KEY",
		"OPENAI_API_KEY=sk-proj-xxxxxxxxxxxxxxxx",
		"API_KEY=xxxx-xxxx-xxxx-xxxx",
		"API_KEY=sk_test_...",
		`export ANTHROPIC_API_KEY="sk-ant-..."`,
		`{"token": "{token}", "api_key": "{API_KEY}"}`,
		`"api_key": "[API_KEY]"`,
		"token: ~/.config/gh/token",
		"aws_secret_access_key = wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		"DATABASE_URL=postgres://postgres:postgres@localhost:5432/db",
		"amqp://guest:guest@localhost",
		"mysql://root:root@127.0.0.1/db",
		"postgres://app:Zx9wLm2Qp7@localhost/app",
		"self.password = password",
		"password: hasLength({ min: 8 })",
		"PWD=/home/user/project",
		"spring.datasource.password=${DB_PASSWORD}",
		`<on PASS: "The review found nothing blocking. Move to the next stage.">`,
		`<on PASS: "Record findings without blocking work. Keep reviewing all modules.">`,
		"const styleToken = styles.find(t =>\n  t.name === name);",
		"  const token = `contrast-check-${Date.now()}-${Math.random().toString(36).slice(2)}`;",
		"Use --password to supply it, or set --password when you run it.",
		"const creds = { username: username, pass: password }",
		"postgres://app:Zx9wLm2Qp7@[::1]/db",
		`password: "......"`,
		`description = "The flag sets the password for the admin account. Use it once."`,
		`export ANTHROPIC_API_KEY="sk-ant-api03-Zx9wLm2Qp7Rt4Kv8..."`,
		"Use sk-ant-api03-Zx9wLm2Qp7Rt4Kv8… as the key.",
		`api_key = f"{provider}_{region}"`,
		`token = f"{prefix}-{uuid4()}"`,
		"const creds = {\n  password: somePassword,\n  user: userName,\n};",
		"login(password=pw, user=u)",
	} {
		if found := secretFindings([]byte(s)); len(found) > 0 {
			t.Errorf("flagged as a credential: %q (%s)", s, found[0].what)
		}
	}
}

func TestCredentialsUnderAnyNameAreFound(t *testing.T) {
	for _, s := range []string{
		"PGPASSWORD=Zx9wLm2Qp7Rt4Kv8 psql -h db",
		"rootPassword: Hunter2Horse9",
		"dbPassword: Zx9wLm2Qp7",
		"adminPassword = Zx9wLm2Qp7",
		"DBPASSWORD=Zx9wLm2Qp7",
		"userPassword: Zx9wLm2Qp7",
		"slackToken = Zx9wLm2Qp7Rt4Kv8",
		"apiToken: Zx9wLm2Qp7Rt4Kv8",
		"mySecret: Zx9wLm2Qp7Rt4Kv8",
		"MYSQL_PWD=Zx9wLm2Qp7",
		"DB_PASS=Zx9wLm2Qp7",
		"SECRET_KEY_BASE=9f2c4e1a7b3d5f8e0a2c4e6b8d0f1a3c",
		"ENCRYPTION_KEY=Zx9wLm2Qp7Rt4Kv8",
		"DefaultEndpointsProtocol=https;AccountName=acct;AccountKey=Zx9wLm2Qp7Rt4Kv8Yb3N==;EndpointSuffix=core.windows.net",
		"Endpoint=sb://x.servicebus.windows.net/;SharedAccessKeyName=root;SharedAccessKey=Zx9wLm2Qp7Rt4Kv8Yb3N=",
		"curl -H 'Ocp-Apim-Subscription-Key: 9f2c4e1a7b3d5f8e0a2c4e6b8d0f1a3c'",
		"password:\n  Zx9wLm2Qp7Rt4Kv8\n",
		`curl "https://x/api?token=${TOKEN}&password=Zx9wLm2Qp7Rt4Kv8"`,
		`api_key=b"Zx9wLm2Qp7Rt4Kv8"`,
		`token = f"Zx9wLm2Qp7Rt4Kv8"`,
		`password = """Zx9wLm2Qp7Rt4Kv8"""`,
		"'api_key' => 'abcdef123456789',",
		"Server=db;Database=app;User Id=sa;Pwd=Zx9wLm2Qp7;",
		"-----BEGIN PGP PRIVATE KEY BLOCK-----\nlQOYBF0\n-----END PGP PRIVATE KEY BLOCK-----",
		"stripe: sk_live_4eC39HqLyjWDarjtT1zdp7dc",
		"rk_live_4eC39HqLyjWDarjtT1zdp7dc",
		"glpat-Zx9wLm2Qp7Rt4Kv8Yb3N",
		"hf_Zx9wLm2Qp7Rt4Kv8Yb3NcD5eF6gH7iJ8",
		"SG.Zx9wLm2Qp7Rt4Kv8Yb3NcD.Zx9wLm2Qp7Rt4Kv8Yb3NcD5eF6gH7iJ8kL9mN0oP1q",
		"ASIA4QWERTYUIOPASDFG",
		"https://hooks.slack.com/services/T0123ABCD/B0123ABCD/Zx9wLm2Qp7Rt4Kv8Yb3N",
		"password: Zx9(wLm2Qp7",
		"password: hunter.horse",
		`password := "Zx9wLm2Qp7Rt4Kv8"`,
		"export PASSWORD=Zx9wLm2Qp7;",
		"spring.datasource.password=Zx9wLm2Qp7",
		"Password: hunter2",
		"password = \"\"\"\nZx9wLm2Qp7Rt4Kv8\n\"\"\"",
		"password:\n  \"correct horse battery staple\"\n",
		"db: {password: admin}",
		"db: {user: app, password: admin}",
		"Server=db;Password = Zx9wLm2Qp7;",
		"Server=db;Pwd = Zx9wLm2Qp7;",
		"curl --password admin https://x",
		`password: "!@#$%^&*()"`,
		`password = "correct horse battery staple tonight!"`,
		"postgres://app:Zx9wLm2Qp7@[2001:db8::1]/db",
		"password: &dbpass Zx9wLm2Qp7Rt4Kv8",
		`password: &dbpass "correct horse battery staple"`,
		"PASSWORD=Zx9wLm2&Qp7Rt4Kv8",
		"$password = 111111;",
		"password := 111111",
		"password: Abc9,Def8",
		"password: Abc9}",
		`api_key = f"Zx9wLm2Qp7Rt4Kv8"`,
	} {
		if !literalSecret([]byte(s)) {
			t.Errorf("credential not found: %q", s)
		}
	}
}

func TestCredentialMessagesNameTheLineAndKeyOnly(t *testing.T) {
	got := secretWhere([]byte("# Setup\n\nPGPASSWORD=Zx9wLm2Qp7Rt4Kv8 psql\ntoken: ghp_Zx9wLm2Qp7Rt4Kv8Yb3N\n"))
	if got != " (line 3: PGPASSWORD, and 1 more)" {
		t.Fatalf("where: %q", got)
	}
	if secretWhere([]byte("Nothing here.")) != "" {
		t.Fatal("where for text without credentials")
	}
}

func TestRedactionHidesOverlappingSpansToTheirEnd(t *testing.T) {
	text := "password: \"x -----BEGIN PRIVATE KEY-----\nMIIEowIBAAKCAQEAzx9wLm2Qp7\n-----END PRIVATE KEY-----\nname: kept\n"
	if got := redactSecrets(text); strings.Contains(got, "MIIEowIBAAKCAQEAzx9wLm2Qp7") || !strings.Contains(got, "name: kept") {
		t.Fatalf("preview: %q", got)
	}
}

func TestCredentialScanIsLinear(t *testing.T) {
	small := strings.Repeat("const f = token =>\n  x;\npassword: |\n  ok\n", 500)
	large := strings.Repeat(small, 8)
	time0 := time.Now()
	literalSecret([]byte(small))
	smallTime := time.Since(time0)
	time0 = time.Now()
	literalSecret([]byte(large))
	// Eight times the text may take somewhat more than eight times as long, but not sixty-four.
	if largeTime := time.Since(time0); largeTime > 30*max(smallTime, time.Millisecond) {
		t.Fatalf("8x the text took %v, against %v", largeTime, smallTime)
	}
}

func TestLineNumbersAreCountedInOrder(t *testing.T) {
	text := "a\nPGPASSWORD=Zx9wLm2Qp7Rt4Kv8\n\n\ntoken: ghp_Zx9wLm2Qp7Rt4Kv8Yb3N\nb\nMYSQL_PWD=Zx9wLm2Qp7\n"
	var lines []int
	for _, f := range secretFindings([]byte(text)) {
		lines = append(lines, f.line)
	}
	if fmt.Sprint(lines) != "[2 5 7]" {
		t.Fatalf("lines: %v", lines)
	}
	many := strings.Repeat("PGPASSWORD=Zx9wLm2Qp7Rt4Kv8\n", 4000)
	start := time.Now()
	if found := secretFindings([]byte(many)); len(found) != 4000 || found[3999].line != 4000 {
		t.Fatalf("findings: %d", len(found))
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("4000 findings took %v", took)
	}
}

func TestValuesAreRedactedWhole(t *testing.T) {
	for _, text := range []string{"PASSWORD=Zx9wLm2&Qp7Rt4Kv8\n", "password: Abc9,Def8xQ\n", "password: &dbpass Zx9wLm2Qp7Rt4Kv8\n"} {
		got := redactSecrets(text)
		if strings.Contains(got, "Qp7Rt4Kv8") || strings.Contains(got, "Def8xQ") {
			t.Fatalf("preview of %q: %q", text, got)
		}
	}
}

func TestManyCredentialsOnOneLineScanInLinearTime(t *testing.T) {
	field := "Password=Zx9wLm2Qp7;"
	small := "Server=db;" + strings.Repeat(field, 2000)
	large := "Server=db;" + strings.Repeat(field, 16000)
	time0 := time.Now()
	if n := len(secretFindings([]byte(small))); n != 2000 {
		t.Fatalf("findings: %d", n)
	}
	smallTime := time.Since(time0)
	time0 = time.Now()
	secretFindings([]byte(large))
	// Linear is about 8 times as long; reading each line again for every field is about 64.
	if largeTime := time.Since(time0); largeTime > 20*max(smallTime, time.Millisecond) {
		t.Fatalf("8x the fields took %v, against %v", largeTime, smallTime)
	}
}
