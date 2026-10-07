import { readFile, readdir, access } from 'node:fs/promises';
import { spawnSync } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
let failures = 0;
function assert(condition, message) { if (!condition) { console.error(message); failures++; } }
async function walk(folder) {
  const entries = await readdir(folder, { withFileTypes: true });
  return (await Promise.all(entries.map(e => e.isDirectory() ? walk(path.join(folder, e.name)) : path.join(folder, e.name)))).flat();
}
for (const file of await walk(path.join(root, 'src'))) {
  if (!file.endsWith('.js')) continue;
  const content = await readFile(file, 'utf8');
  assert(spawnSync(process.execPath, ['--check', file]).status === 0, `Syntax error: ${path.relative(root, file)}`);
  if (!file.endsWith('browser-api.js')) assert(!/\b(?:chrome|browser)\s*\./.test(content), `Direct platform API outside compatibility layer: ${file}`);
  assert(!/console\.(?:log|debug|info|warn|error)\s*\(/.test(content), `Unexpected logging in extension: ${file}`);
  assert(!/storage\.sync|\beval\s*\(|new Function\s*\(/.test(content), `Unsafe storage/evaluation: ${file}`);
}
for (const target of ['chrome', 'firefox']) {
  const folder = path.join(root, 'dist', target);
  const manifest = JSON.parse(await readFile(path.join(folder, 'manifest.json'), 'utf8'));
  const packageVersion = JSON.parse(await readFile(path.join(root, 'package.json'), 'utf8')).version;
  assert(manifest.version === packageVersion, `${target}: version differs from package.json`);
  assert(manifest.manifest_version === 3, `${target}: not MV3`);
  assert(manifest.permissions.join(',') === 'tabs,webNavigation,storage,notifications,alarms', `${target}: permissions changed`);
  const required = [manifest.action.default_popup, manifest.options_page || manifest.options_ui.page,
    ...Object.values(manifest.icons), ...(target === 'chrome' ? [manifest.background.service_worker] : manifest.background.scripts)];
  for (const resource of required) try { await access(path.join(folder, resource)); } catch { assert(false, `${target}: missing ${resource}`); }
  for (const prefix of ['icons/', 'icons/recording/']) for (const size of [16, 32, 48, 64, 128]) {
    try { await access(path.join(folder, `${prefix}${size}.png`)); } catch { assert(false, `${target}: missing action icon ${prefix}${size}.png`); }
  }
  assert(spawnSync(process.execPath, ['--check', path.join(folder, 'background.js')]).status === 0, `${target}: invalid bundle`);
  assert(manifest.content_security_policy.extension_pages.includes('connect-src ws://127.0.0.1:*'), `${target}: WebSocket CSP missing`);
}
if (failures) process.exit(1);
console.log('Syntax, manifests, packaged resources and extension security checks passed.');
