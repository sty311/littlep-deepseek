# Security

Never submit API keys, authorization headers, cookies, device tokens, serial numbers, Wi-Fi credentials, private scans or full device logs. Keep `private/`, `backup/`, `payload/` and runtime config outside version control. Example configuration has a placeholder only.

The bridge binds only to an explicit loopback IP; integrated deployment uses 127.0.0.1:18181. It is not an authenticated LAN service. Local processes/root can access it and its key. HTTPS verification remains enabled for upstream calls; no CA, DNS or global TLS modification is part of installation.

The installer refuses unsupported identities or unknown hashes, saves a verified private backup and requires explicit confirmation. Do not circumvent these checks. Review all user-supplied compilers and private input provenance. Memory sessions and diagnostic hashes are still private usage data.

After the repository is published, please open a private security advisory for security issues. No dedicated reporting address is defined at this local packaging stage. Do not disclose secrets in a public issue.
