'use strict';

// Public files only. This server never reads the user's local setup inventory.
const http = require('node:http');
const fs = require('node:fs');
const path = require('node:path');
const { randomUUID } = require('node:crypto');
const root = fs.realpathSync(__dirname);
const pages = new Set(['/index.html', '/404.html', '/style.css', '/app.js', '/icon.svg']);
const types = { '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.svg': 'image/svg+xml', '.json': 'application/json; charset=utf-8', '.md': 'text/plain; charset=utf-8', '.txt': 'text/plain; charset=utf-8', '.ps1': 'text/plain; charset=utf-8', '.command': 'text/plain; charset=utf-8', '.cmd': 'application/octet-stream', '.exe': 'application/octet-stream', '.zip': 'application/zip' };
const headers = {
  'X-Content-Type-Options': 'nosniff',
  'Referrer-Policy': 'no-referrer',
  'Content-Security-Policy': "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'; connect-src 'none'",
  'Cache-Control': 'no-cache',
};

async function handle(req, res) {
  if (!['GET', 'HEAD'].includes(req.method)) {
    res.writeHead(405, { ...headers, Allow: 'GET, HEAD' });
    res.end();
    return;
  }
  let pathname;
  try {
    pathname = decodeURIComponent(new URL(req.url, 'http://localhost').pathname);
  } catch {
    res.writeHead(400, headers); res.end('Invalid path'); return;
  }
  if (pathname === '/guide.html') { res.writeHead(301, { ...headers, Location: '/' }); res.end(); return; }
  // Links to the retired .cmd installer still get the Windows setup.
  if (pathname === '/downloads/Install-Copilot.cmd') { res.writeHead(302, { ...headers, Location: '/downloads/Install-Copilot-Windows.zip' }); res.end(); return; }
  if (pathname === '/') pathname = '/index.html';
  if (pathname.includes('\\') || pathname.includes('\0') ||
      !(pages.has(pathname) || pathname.startsWith('/downloads/'))) {
    return notFound(req, res);
  }
  const candidate = path.resolve(root, '.' + pathname);
  if (!candidate.startsWith(root + path.sep)) return notFound(req, res);
  try {
    const real = await fs.promises.realpath(candidate);
    if (!real.startsWith(root + path.sep)) return notFound(req, res);
    // The allowlist applies to the normalized target too, preventing traversal into server code.
    const relative = '/' + path.relative(root, real).split(path.sep).join('/');
    if (!(pages.has(relative) || relative.startsWith('/downloads/'))) return notFound(req, res);
    const stat = await fs.promises.stat(real);
    if (!stat.isFile()) return notFound(req, res);
    const ext = path.extname(real).toLowerCase();
    const response = { ...headers, 'Content-Type': types[ext] || 'application/octet-stream', 'Content-Length': stat.size };
    if (['.cmd', '.exe', '.zip'].includes(ext) || relative.startsWith('/downloads/') && !ext) {
      response['Content-Disposition'] = 'attachment; filename="' + path.basename(real).replace(/["\r\n]/g, '_') + '"';
    }
    if (/^\/downloads\/\d+\.\d+\.\d+(?:-[A-Za-z0-9.-]+)?\//.test(relative)) response['Cache-Control'] = 'public, max-age=31536000, immutable';
    res.writeHead(200, response);
    if (req.method === 'HEAD') return res.end();
    fs.createReadStream(real).on('error', () => { res.downloadReadFailed = true; res.destroy(); }).pipe(res);
  } catch {
    return notFound(req, res);
  }
}

function notFound(req, res) {
  res.writeHead(404, { ...headers, 'Content-Type': 'text/html; charset=utf-8' });
  if (req.method === 'HEAD') return res.end();
  const page = path.join(root, '404.html');
  const stream = fs.createReadStream(page);
  stream.on('error', () => res.end('<h1>Not found</h1><a href="/">Copilot Ready</a>'));
  stream.pipe(res);
}

// Telemetry is best effort: logging failures must never stop a download.
process.stdout.on('error', () => {});
function logEvent(event) {
  try { process.stdout.write(JSON.stringify(event) + '\n', () => {}); } catch {}
}
async function trackDownload(req, res) {
  let pathname;
  try { pathname = decodeURIComponent(new URL(req.url, 'http://localhost').pathname); } catch { return; }
  const platform = ['/downloads/Install-Copilot-Windows.zip', '/downloads/Install-Copilot-Windows-ARM64.zip'].includes(pathname) ? 'windows' : pathname === '/downloads/Install-Copilot-macOS.zip' ? 'macos' : null;
  if (req.method !== 'GET' || !platform) return;
  const event = { event: 'installer_download', id: randomUUID(), timestamp: new Date().toISOString(), platform, release: 'unknown' };
  res.setHeader('X-Download-Event-ID', event.id);
  let recorded = false;
  const record = (finished) => {
    if (recorded) return;
    recorded = true;
    logEvent({ ...event, status: res.statusCode, outcome: res.downloadReadFailed || res.statusCode >= 400 ? 'failed' : finished ? 'completed' : 'interrupted' });
  };
  res.once('finish', () => record(true));
  res.once('close', () => record(res.writableFinished));
  try { event.release = JSON.parse(await fs.promises.readFile(path.join(root, 'downloads/manifest.json'), 'utf8')).releaseVersion || 'unknown'; } catch {}
}
const server = http.createServer(async (req, res) => {
  try { await trackDownload(req, res); } catch {}
  handle(req, res).catch(() => { if (!res.headersSent) res.writeHead(500, headers); res.end(); });
});
logEvent({ event: 'download_monitor_started', timestamp: new Date().toISOString() });
server.listen(Number(process.env.PORT) || 8080, '0.0.0.0');
