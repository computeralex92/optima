# Security policy

## Reporting a vulnerability

Please report vulnerabilities privately through GitHub's private vulnerability
reporting (Security tab → *Report a vulnerability*) rather than in a public
issue. Include the affected endpoint or component and steps to reproduce. You
should receive a response within a few days.

## Scope

The service exposes an unauthenticated HTTP API on port 7050 and is intended to
run on a trusted network behind evcc. Do not expose it directly to the internet.

`/optimize/explain` sends the plan and prompt to the configured external LLM
provider. Leave the provider settings unset to disable that endpoint.
