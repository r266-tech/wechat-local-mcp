# Security

wx-mcp is a local-first tool for reading WeChat data on a Mac controlled by the
user running it. It does not require or use a remote service.

## Sensitive Local State

The following files are intentionally local and must not be committed or shared:

- `~/.config/wxcli/config.json`: contains the wxid, DB root, and DB key material.
- `~/.wx-mcp/cache/`: contains plaintext snapshot DBs and `index.sqlite`.
- macOS Keychain item `r266.wx-mcp.sudo`: contains the user's stored sudo password for unattended no-SIP key refresh.
- `dist/`, `wx-mcp`, `wxkey`, and local `libWCDB.dylib` build artifacts.

The repository `.gitignore` excludes the common local artifacts, but users should
still review `git status --short` before publishing changes.

## Permissions

wx-mcp reads local WeChat databases in readonly mode. First-run key extraction is
handled by the companion `wxkey` CLI and requests administrator privileges to
read the local WeChat process memory. The supported path is no-SIP only:
`wxkey bootstrap` stores the user's sudo password in macOS Keychain, ad-hoc
signs the local WeChat.app when macOS denies `task_for_pid`, and future key
refreshes reuse the Keychain credential through `sudo -S`.

Run these commands when diagnosing a new machine:

```bash
./install.sh --doctor --json
./wxkey doctor
./wx-mcp cache status
```

## Reporting Issues

Please avoid including message contents, DB keys, raw `config.json`, plaintext
cache files, or personal identifiers in public issues. Redact local paths and
account identifiers when possible.
