import { build } from 'esbuild';
import { mkdir, writeFile, rename } from 'node:fs/promises';
import { pathToFileURL } from 'node:url';
import path from 'node:path';

const root = path.resolve(import.meta.dirname, '..');
await mkdir(path.join(root, 'build'), { recursive: true });
const shared = { absWorkingDir: root, bundle: true, jsx: 'automatic', define: { 'process.env.NODE_ENV': '"production"' }, legalComments: 'inline', logLevel: 'info' };
await build({ ...shared, entryPoints: ['website/main.jsx'], outfile: 'site/app.js', minify: true, target: ['es2020'], format: 'iife' });
await rename(path.join(root, 'site/app.css'), path.join(root, 'site/style.css'));
await build({ ...shared, entryPoints: ['website/render.jsx'], outfile: 'build/site-render.mjs', platform: 'node', format: 'esm', packages: 'external', logLevel: 'silent' });
const { render } = await import(pathToFileURL(path.join(root, 'build/site-render.mjs')).href);
{
  const title = 'Your setup. Ready for Copilot.';
  const description = 'Bring your settings from Codex and Claude Code. Choose what to keep. Set up Copilot on Windows or Mac with one download.';
  const html = `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="theme-color" content="#090b0f"><meta name="description" content="${description}"><title>${title}</title><link rel="icon" href="/icon.svg" type="image/svg+xml"><link rel="stylesheet" href="/style.css"><script defer src="/app.js"></script></head><body><div id="root">${render()}</div><noscript><div class="shell"><p>JavaScript is off. Download <a href="/downloads/Install-Copilot-Windows.zip" download>Windows setup</a> (<a href="/downloads/Install-Copilot-Windows-ARM64.zip" download>ARM64</a>) or <a href="/downloads/Install-Copilot-macOS.zip" download>macOS setup</a>.</p></div></noscript></body></html>`;
  await writeFile(path.join(root, 'site', 'index.html'), html);
}
console.log('Built the single-page React site. Downloads remain available without JavaScript.');
