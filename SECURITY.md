# Security Policy

dockmesh takes security seriously. This document describes how to report security issues and what to expect in response.

## Supported Versions

Only the most recent minor release receives security updates.

| Version | Supported |
|---------|-----------|
| 1.0.x   | ✅        |
| < 1.0   | ❌        |

## Reporting a Vulnerability

**Do not open a public GitHub Issue for security vulnerabilities.**

Send reports to **security@dockmesh.dev** with:

- A description of the vulnerability
- Steps to reproduce or a minimal proof of concept
- The dockmesh version affected
- Your contact information (optional — you may report anonymously)
- Whether you want credit in the release notes

If the issue affects the agent protocol, mTLS certificate handling, RBAC enforcement, SSO/OIDC validation, or session management, please flag that clearly in the subject line.

## What to Expect

- **Acknowledgement:** within 72 hours of your report
- **Initial assessment:** within 7 days — severity classification and whether a fix is needed
- **Fix timeline:** depends on severity, typically 2–30 days for critical/high severity
- **Disclosure:** coordinated with you before any public announcement
- **Credit:** we'll credit you in release notes (by name, handle, or anonymously — your choice)

dockmesh is a community-maintained project. Maintainers work on this in their spare time, and there is no commercial support SLA. We do our best to respond quickly to security reports, but response times depend on volunteer availability.

## Scope

**In scope:**

- The dockmesh server binary
- The agent binary
- The embedded SvelteKit UI
- Documented APIs and protocols (REST, agent mTLS WebSocket)
- Default configurations

**Out of scope:**

- Issues in third-party dependencies that don't affect dockmesh's use of them (report those upstream)
- Social engineering of users or maintainers
- Physical access attacks
- Denial-of-service via resource exhaustion without a specific exploit (these are operational limits, not vulnerabilities)
- Vulnerabilities in self-compiled forks or modifications

## Safe Harbor

We consider security research performed in good faith and in accordance with this policy to be authorized. We will not pursue legal action against researchers who:

- Follow the responsible disclosure process above
- Do not access, modify, or destroy data beyond what's necessary to demonstrate the issue
- Do not attempt to exploit vulnerabilities against production dockmesh installations that aren't their own
- Give us reasonable time to fix the issue before public disclosure

## Disclosure Timeline

Our default disclosure window is **90 days** from the initial report. If a fix takes longer, we'll coordinate with you on a revised timeline. We will not publicly disclose details of an unfixed vulnerability without your consent.

## Hall of Fame

Security researchers who have responsibly disclosed issues (with their permission):

_The project is new — no disclosures yet. Your name could be here._

---

Thank you for helping keep dockmesh and its users safe.
