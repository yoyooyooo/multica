package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelfhostWorkloadAssertionIssuerFailsClosed(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	read := func(relative string) string {
		t.Helper()
		contents, err := os.ReadFile(filepath.Join(repoRoot, relative))
		if err != nil {
			t.Fatalf("read %s: %v", relative, err)
		}
		return string(contents)
	}

	envExample := read(".env.example")
	if !strings.Contains(envExample, "MULTICA_WORKLOAD_ASSERTION_ISSUER=\n") ||
		!strings.Contains(envExample, "MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=\n") ||
		strings.Contains(envExample, "MULTICA_WORKLOAD_ASSERTION_ISSUER=multica") {
		t.Fatal(".env.example must leave both workload assertion identity values unset")
	}

	compose := read("docker-compose.selfhost.yml")
	if !strings.Contains(compose, "MULTICA_WORKLOAD_ASSERTION_ISSUER: ${MULTICA_WORKLOAD_ASSERTION_ISSUER:?") ||
		!strings.Contains(compose, "MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID: ${MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID:?") ||
		strings.Contains(compose, "MULTICA_WORKLOAD_ASSERTION_ISSUER:-multica") {
		t.Fatal("selfhost compose must require explicit workload assertion identity")
	}
	mainSource := read("server/cmd/server/main.go")
	if !strings.Contains(mainSource, "handler.ValidateWorkloadAssertionConfiguration(") ||
		!strings.Contains(mainSource, "os.Getenv(\"MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID\")") {
		t.Fatal("server startup must validate both workload assertion identity values before opening the database")
	}
	healthSource := read("server/cmd/server/health.go")
	if !strings.Contains(healthSource, "WorkloadAssertionIdentity") || !strings.Contains(healthSource, "ValidateWorkloadAssertionConfiguration") {
		t.Fatal("readiness must report workload assertion identity validation")
	}
	helmConfig := read("deploy/helm/multica/templates/configmap.yaml")
	if !strings.Contains(helmConfig, "required \"backend.config.workloadAssertionIssuer") ||
		!strings.Contains(helmConfig, "workloadAssertionIssuerInstanceId") ||
		!strings.Contains(helmConfig, "MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID:") {
		t.Fatal("Helm must validate and pass both workload assertion identity values")
	}
	helmValues := read("deploy/helm/multica/values.yaml")
	if !strings.Contains(helmValues, "workloadAssertionIssuer: \"\"") || !strings.Contains(helmValues, "workloadAssertionIssuerInstanceId: \"\"") {
		t.Fatal("Helm values must not ship shared workload assertion identity defaults")
	}

	doc := read("docs/features/fork/external-pr-integration/README.md")
	if !strings.Contains(doc, "urn:multica:deployment:<stable-instance-id>") ||
		strings.Contains(doc, "MULTICA_WORKLOAD_ASSERTION_ISSUER=multica") {
		t.Fatal("fork documentation must use a deployment-unique issuer example")
	}
	selfHosting := read("SELF_HOSTING.md")
	if !strings.Contains(selfHosting, "MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:deployment:<stable-instance-id>") ||
		!strings.Contains(selfHosting, "bash scripts/validate-workload-issuer.sh .env") ||
		!strings.Contains(selfHosting, "docker compose -f docker-compose.selfhost.yml config >/dev/null") {
		t.Fatal("main self-host guide must set and validate the deployment-unique issuer before startup")
	}
	makefile := read("Makefile")
	if !strings.Contains(makefile, "selfhost: selfhost-env") ||
		!strings.Contains(makefile, "selfhost-build: selfhost-env") ||
		!strings.Contains(makefile, "bash scripts/ensure-workload-issuer.sh .env deployment") ||
		!strings.Contains(makefile, "bash scripts/validate-workload-issuer.sh .env") {
		t.Fatal("make selfhost entry points must share the supported create/upgrade issuer preflight")
	}
	for _, relative := range []string{"scripts/dev.sh", "scripts/install.sh", "scripts/install.ps1"} {
		contents := read(relative)
		if !strings.Contains(contents, "urn:multica:deployment:") && !strings.Contains(contents, "urn:multica:development:") {
			t.Fatalf("%s must persist a generated issuer", relative)
		}
		if !strings.Contains(contents, "MULTICA_WORKLOAD_ASSERTION_ISSUER") || !strings.Contains(contents, "MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID") {
			t.Fatalf("%s does not reconcile both workload assertion identity settings", relative)
		}
	}
	for _, tc := range []struct {
		name, initial, want string
	}{
		{name: "missing", want: "MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:worktree:"},
		{name: "empty", initial: "MULTICA_WORKLOAD_ASSERTION_ISSUER=\n", want: "MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:worktree:"},
		{name: "whitespace", initial: "MULTICA_WORKLOAD_ASSERTION_ISSUER=   \n", want: "MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:worktree:"},
		{name: "placeholder", initial: "MULTICA_WORKLOAD_ASSERTION_ISSUER=multica\n", want: "MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:worktree:"},
		{name: "valid", initial: "MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:worktree:keep-me\n", want: "MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:worktree:keep-me"},
	} {
		t.Run("worktree_issuer_"+tc.name, func(t *testing.T) {
			generatedEnv := filepath.Join(t.TempDir(), ".env.worktree")
			if tc.initial != "" {
				if err := os.WriteFile(generatedEnv, []byte(tc.initial), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("bash", filepath.Join(repoRoot, "scripts", "init-worktree-env.sh"), generatedEnv)
			cmd.Dir = repoRoot
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("generate worktree env: %v: %s", err, output)
			}
			generated, err := os.ReadFile(generatedEnv)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(generated), tc.want) || !strings.Contains(string(generated), "MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=multica-") || strings.Contains(string(generated), "MULTICA_WORKLOAD_ASSERTION_ISSUER=multica\n") {
				t.Fatalf("worktree workload assertion identity %s did not converge: %s", tc.name, generated)
			}
		})
	}

	quickstarts := []string{
		"apps/docs/content/docs/self-host-quickstart.mdx",
		"apps/docs/content/docs/self-host-quickstart.zh.mdx",
		"apps/docs/content/docs/self-host-quickstart.ja.mdx",
		"apps/docs/content/docs/self-host-quickstart.ko.mdx",
	}
	for _, relative := range quickstarts {
		contents := read(relative)
		if !strings.Contains(contents, "MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:deployment:<stable-instance-id>") ||
			!strings.Contains(contents, "MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=<ags-trusted-issuer-id>") ||
			!strings.Contains(contents, "docker compose -f docker-compose.selfhost.yml config >/dev/null") ||
			!strings.Contains(contents, "--set-string 'backend.config.workloadAssertionIssuer=urn:multica:deployment:<stable-instance-id>'") ||
			!strings.Contains(contents, "--set-string 'backend.config.workloadAssertionIssuerInstanceId=<ags-trusted-issuer-id>'") {
			t.Fatalf("%s must preserve the issuer in executable Compose and Helm commands", relative)
		}
	}
	selfHostingZH := read("apps/docs/content/docs/getting-started/self-hosting.zh.mdx")
	if !strings.Contains(selfHostingZH, "MULTICA_WORKLOAD_ASSERTION_ISSUER=urn:multica:deployment:<stable-instance-id>") ||
		!strings.Contains(selfHostingZH, "MULTICA_WORKLOAD_ASSERTION_ISSUER_INSTANCE_ID=<ags-trusted-issuer-id>") ||
		!strings.Contains(selfHostingZH, "docker compose -f docker-compose.selfhost.yml config >/dev/null") {
		t.Fatal("Chinese self-host guide must preserve the issuer and validate config before upgrade")
	}
	for _, relative := range []string{
		"apps/docs/content/docs/github-integration.mdx",
		"apps/docs/content/docs/github-integration.zh.mdx",
		"apps/docs/content/docs/github-integration.ja.mdx",
		"apps/docs/content/docs/github-integration.ko.mdx",
		"apps/docs/content/docs/vcs-integration.mdx",
		"apps/docs/content/docs/vcs-integration.zh.mdx",
	} {
		contents := read(relative)
		if !strings.Contains(contents, "leaf child") || !strings.Contains(contents, "open") || !strings.Contains(contents, "draft") {
			t.Fatalf("%s must document the current provider completion boundary", relative)
		}
	}
}
