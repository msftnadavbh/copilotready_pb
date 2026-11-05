# Download monitoring

The optional static server emits JSON records for installer ZIP responses. This repository provides no hosted instance or collected logs.

To summarize logs you already have locally, use Python 3:

```sh
python3 scripts/download_report.py --logs /path/to/diagnostics.zip
python3 scripts/download_report.py --logs /path/to/diagnostics.zip --days 30 --csv downloads.csv
```

`--logs` is required. The report reads `.log` and `.txt` entries in that ZIP; it does not sign in, acquire tokens, fetch remote logs, or contact a cloud service. An optional `--smoke-ids /path/to/checks.json` accepts a JSON array of event IDs to group separately as deployment checks. An explicitly supplied missing file is an error. Without this option, all matching responses are grouped as downloads. Duplicate log records are counted once.

Counts are grouped by UTC day, platform, release, and outcome. Completed means the server finished its response, not that someone saved the file or installed anything. Repeats, retries, and bots can count separately. HEAD requests, helpers, and other downloads do not count. Range headers retain the server's full-file response behavior.

Each event contains a random event ID, timestamp, platform, release, HTTP status, and outcome, with no visitor identifier. The ID is also returned as `X-Download-Event-ID`. The report shows the observed record interval: rotation, buffering, storage limits, and process termination can leave gaps, so missing history is unknown rather than zero. Logging errors do not block responses. Hosting infrastructure may retain its own access logs; this report does not inspect or configure them.
