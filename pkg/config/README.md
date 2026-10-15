# Configuration

This project supports an optional YAML config file at:

```text
<data-dir>/config.yaml
```

The server checks this file at startup if it exists. Validation problems are logged, but they do not stop the server from starting.

## Example

```yaml
sources:
  - id: src_7f4a2c91b0d4e8f123456789
    name: Work GitHub
    platform: github
    username: octocat
    token: $encrypted$1$REPLACE_WITH_ENCRYPTED_VALUE
    only_include_repos:
      - hello-world
    exclude_repos:
      - archived-repo
    include_defaults: true
    include_starred: false
    include_watching: false
    issues_enabled: false
concurrency: 5
max_retry_times: 2
cron: '0 * * * *'
```

## Top-level fields

### `sources`

- Type: array of source objects
- Required: no, but having no configured sources produces a `warning`

Each source supports the following fields:

| Field                | Type     | Required | Notes                                                   |
| -------------------- | -------- | -------- | ------------------------------------------------------- |
| `id`                 | string   | yes      | System-generated stable identifier                      |
| `name`               | string   | no       | Optional display name / remark                          |
| `platform`           | string   | yes      | v1 only supports `github`                               |
| `username`           | string   | yes      | Git provider username                                   |
| `token`              | string   | yes      | Must use `$encrypted$1$...` format                      |
| `only_include_repos` | string[] | no       | Optional allowlist                                      |
| `exclude_repos`      | string[] | no       | Optional blocklist                                      |
| `include_defaults`   | bool     | no       | Default `true`                                          |
| `include_starred`    | bool     | no       | Default `false`                                         |
| `include_watching`   | bool     | no       | Default `false`                                         |
| `issues_enabled`     | bool     | no       | Default `false`; back up Issues for active repositories |

Meaning of the include fields:

- `include_defaults`: include repositories you can access by default, including your own repositories and organization repositories
- `include_starred`: include repositories you have starred
- `include_watching`: include repositories you are watching

### `concurrency`

- Type: integer
- Required: no
- Default: `5`
- Validation: if explicitly set, it must be greater than `0`

### `max_retry_times`

- Type: integer
- Required: no
- Default: `2`
- Meaning: retry up to 2 times after the first attempt, for a total of up to 3 attempts
- Validation: if explicitly set, it must be greater than or equal to `0`

### `cron`

- Type: string
- Required: no
- Validation: if explicitly set, it must be a valid 5-field cron expression
- Parsing: validated with `go-co-op/gocron` using 5-field mode only
- Example: `0 * * * *`

## Validation

Validation issues have three severity levels:

- `error`: the config is invalid
- `warning`: the config is suspicious but still accepted
- `info`: reserved for future use

If `name` is omitted in `config.yaml`, API responses fall back to the source `id` for display purposes.

### Current `error` rules

- Invalid YAML syntax or decode failure
- Missing required source fields: `id`, `platform`, `username`, `token`
- Unsupported `platform` value
- Duplicate source `id`
- `concurrency <= 0`
- `max_retry_times < 0`
- Invalid `cron` expression
- Source-specific check requested for a non-existent source ID
- `token` is plain text instead of `$encrypted$1$...`
- Encrypted `token` payload is malformed
- Encrypted `token` cannot be decrypted with the configured passphrase

### Current `warning` rules

- Unknown fields at the top level or inside a source
- `sources` is empty

## Check RPCs

Config checks are exposed through the Connect RPC service:

```text
gitplus.config.v1.ConfigService
```

The service is mounted under the `/api` base path.

### Check the whole config

- RPC: `CheckConfig`
- Generated client path: `/api/gitplus.config.v1.ConfigService/CheckConfig`
- Request message: `CheckConfigRequest`
- Response message: `CheckConfigResponse`

Response fields:

- `issues`
- `summary`

Example response shape:

```json
{
  "issues": [
    {
      "severity": "SEVERITY_WARNING",
      "code": "unknown_field",
      "message": "field \"unexpected\" is not recognized",
      "path": "sources[0].unexpected",
      "line": 7
    }
  ],
  "summary": {
    "error": 0,
    "warning": 1,
    "info": 0
  }
}
```

### Check one source

- RPC: `CheckSourceConfig`
- Generated client path: `/api/gitplus.config.v1.ConfigService/CheckSourceConfig`
- Request message: `CheckSourceConfigRequest`
- Required request field: `source_id`
- Response message: `CheckSourceConfigResponse`

Response fields:

- `exists`
- `source_id`
- `issues`
- `summary`

### Frontend usage

The frontend should call this service through the generated Connect client and the shared transport in [transport.ts](/Users/wangxuan/Documents/PProjects/git-plus/frontend/src/lib/connect/transport.ts).

Legacy REST-style paths such as `/api/config/check` and `/api/config/sources/{id}/check` are no longer supported.

## Token encryption

`token` values must not be stored as plain text. The config only accepts values in this format:

```text
$encrypted$1$<payload>
```

Set the passphrase with:

```text
ENCRYPTION_PASSPHRASE
```

Generate an encrypted token with the CLI:

```bash
printf '%s' 'ghp_xxx' | ENCRYPTION_PASSPHRASE='your passphrase' git-plus config encrypt-token
```

The command reads the plain token from standard input and prints an encrypted token that can be pasted into `config.yaml`.
The `git-plus` server command also requires `ENCRYPTION_PASSPHRASE` to be present before startup begins; if it is missing, the Cobra command exits immediately.

### Token-related error codes

- `unencrypted_token`
- `invalid_encrypted_token`
- `token_decryption_failed`

## Notes

- The config file is optional in v1.
- The app currently only defines and validates this config. It does not yet use the config to drive business behavior.
- Advanced YAML features are not part of the supported contract. Keep the file to plain mappings, sequences, and scalar values.

Issues backups reuse source sync and cron; see the [Issues backup guide](../../docs/issues-backup.md) for scope, permissions, storage, and recovery.
