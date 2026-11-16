# Security

## Reporting a vulnerability

Email sc2-starlance@proton.me. Please include what you found, how to
reproduce it, and what an attacker could do with it. Do not open a public
issue for a security problem.

There is no bug bounty. This is a fan project run on volunteer time; reports
are read and fixed as fast as possible, and reporters are credited if they
want to be.

## What is in scope

- This app: its source in this repository and the release binaries built
  from it.
- The release pipeline: the workflow files in `.github/workflows` and the
  signing setup they use.

## Release signing

Release zips are signed with minisign. The app embeds the public key in
`app/minisign.pub` and refuses an update whose `.minisig` does not verify, so a
compromised GitHub account cannot push a malicious binary to existing
installs. The secret key lives only in the protected `release` environment
of this repository's GitHub Actions configuration.

The Windows and macOS binaries are not code-signed: Windows SmartScreen and
macOS Gatekeeper warn on first run. That is a known trade-off of a
no-budget project, not a compromise. Verify that a download comes from this
repository's releases page and has a matching `.sha256` and `.minisig`.
