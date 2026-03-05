# Configuration

The operator can be configured via a **config file** and **CLI flags**. Flags override file values.

## Config file

By default the operator looks for a config file at:

- `./config.yaml`
- Or the path set by `--config-path`

The file is optional. Defaults apply for any missing keys. Only include keys you want to override.

## Inspecting effective config

Use `--print-config` to print configuration and exit (no operator run). Modes (comma-separated):

| Mode     | Description                    |
|----------|--------------------------------|
| `defaults` | Built-in defaults              |
| `flags`    | Values from CLI flags only     |
| `user`     | Keys present in the config file|
| `merged`   | Final merged configuration     |

Example:

```bash
./selfhealing-operator --print-config=defaults,merged
```

Output is multi-document YAML with section headers.

## CLI flags

Common flags:

| Flag            | Description                    |
|-----------------|--------------------------------|
| `--config-path` | Path to config file           |
| `--log-level`   | Log level (debug, info, warn, error) |
| `--dry-run`     | Do not perform remediation    |
| `--print-config`| Print config and exit         |
| `--metrics-addr`| Metrics listen address        |
| `--health-addr` | Health/ready listen address   |
| `--leader-elect`| Enable leader election for HA|

Run with `--help` for the full list.

## Log level

Set in config or via flag:

```yaml
logLevel: debug
```

```bash
--log-level=debug
```

When `logLevel` is `debug`, the merged config is logged after load.

## Example config file

```yaml
logLevel: info
metricsAddr: ":8080"
healthAddr: ":8081"
# leaderElection: true
# dryRun: false
```

## Policy dry-run

ClusterRemediationPolicy CRs can enable dry-run mode. When any active policy has dry-run enabled, the operator logs actions but does not execute them. This overrides the global `--dry-run` flag.
