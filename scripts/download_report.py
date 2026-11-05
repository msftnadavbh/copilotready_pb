"""Summarize installer response logs from an explicitly supplied local ZIP."""
import argparse
import collections
import csv
import datetime as dt
import io
import json
from pathlib import Path
import zipfile


def events_from_zip(data):
    seen = set()
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        for name in archive.namelist():
            if not name.endswith(('.log', '.txt')):
                continue
            for line in archive.read(name).decode('utf-8', errors='replace').splitlines():
                start = line.find('{"event":"')
                if start < 0:
                    continue
                try:
                    event, _ = json.JSONDecoder().raw_decode(line[start:])
                    if event.get('event') not in ('installer_download', 'download_monitor_started'):
                        continue
                    key = event.get('id') or ('started', event['timestamp'])
                    if key in seen:
                        continue
                    seen.add(key)
                    yield event
                except (ValueError, KeyError):
                    continue


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--days', type=int, default=7)
    parser.add_argument('--csv', type=Path)
    parser.add_argument('--logs', type=Path, required=True, help='Local diagnostic ZIP to read')
    parser.add_argument('--smoke-ids', type=Path, help='Optional JSON array of deployment-check event IDs')
    args = parser.parse_args()
    if args.days < 1:
        parser.error('--days must be positive')
    data = args.logs.read_bytes()
    smoke = set(json.loads(args.smoke_ids.read_text(encoding='utf-8'))) if args.smoke_ids else set()
    events = list(events_from_zip(data))
    cutoff = dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=args.days)
    counts = collections.Counter()
    observed = []
    for event in events:
        try:
            timestamp = dt.datetime.fromisoformat(event['timestamp'].replace('Z', '+00:00'))
            observed.append(timestamp)
            if timestamp < cutoff or event['event'] != 'installer_download':
                continue
            group = 'deployment check' if event['id'] in smoke else 'downloads'
            counts[(timestamp.date().isoformat(), event['platform'], event['release'], event['outcome'], group)] += 1
        except (KeyError, ValueError, TypeError):
            continue
    print(f'Copilot Ready download responses - last {args.days} days (UTC)')
    print('Observed monitoring records: ' + (f'{min(observed).isoformat()} to {max(observed).isoformat()}' if observed else 'none available yet'))
    print('Logs may be delayed or rotated. Missing periods are unknown, not zero. Counts are responses, not people or installations; repeats and bots may count.')
    columns = ['day_utc', 'platform', 'release', 'outcome', 'group', 'count']
    rows = [list(key) + [count] for key, count in sorted(counts.items())]
    print(' | '.join(columns))
    for row in rows:
        print(' | '.join(map(str, row)))
    if not rows:
        print('No matching download events in the available logs.')
    if args.csv:
        with args.csv.open('w', encoding='utf-8', newline='') as f:
            writer = csv.writer(f)
            writer.writerow(columns)
            writer.writerows(rows)
        print('CSV: ' + str(args.csv.resolve()))


if __name__ == '__main__':
    main()
