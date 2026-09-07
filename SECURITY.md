# Security policy

Do not include tracker credentials, passkeys, API keys, qBittorrent credentials, runtime settings, databases, downloaded media, logs, SSH material, Tizen signing certificates, or webOS Developer Mode pairing material in an issue or commit.

For a sensitive report, use GitHub's private vulnerability reporting feature when it is available on this repository. For a non-sensitive defect, open a regular issue with secrets and private network details removed.

The `master` branch and pull requests run secret, dependency, source, filesystem, and workflow-security checks. Release assets include checksums, SBOMs, and GitHub build-provenance attestations. These checks reduce risk but do not make this unauthenticated private-LAN service safe to expose to the public internet.
