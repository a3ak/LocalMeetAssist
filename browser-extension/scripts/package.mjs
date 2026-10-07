import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { zipDirectory } from './zip.mjs';
import { mkdir, copyFile, readFile, rm } from 'node:fs/promises';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
// Archive names follow the version in package.json, so a bump never leaves an
// old ZIP next to a new one.
const { version } = JSON.parse(await readFile(path.join(root, 'package.json'), 'utf8'));
const packages = path.join(root, 'packages');
await rm(packages, { recursive: true, force: true });
for (const target of ['chrome', 'firefox']) await zipDirectory(path.join(root, 'dist', target), path.join(root, 'artifacts', `localmeetassist-${target}-${version}.zip`));
await mkdir(packages, { recursive: true });
for (const target of ['chrome', 'firefox']) await copyFile(path.join(root, 'artifacts', `localmeetassist-${target}-${version}.zip`), path.join(packages, `localmeetassist-${target}-${version}.zip`));
await zipDirectory(root, path.join(root, 'artifacts', `LocalMeetAssist_Browser_Extensions_${version}.zip`), 'localmeetassist-browser-extension/',
  new Set(['artifacts', 'node_modules', '.git', '.DS_Store', 'Thumbs.db']));
console.log(`Created browser ZIPs and GitHub source ZIP for ${version} in artifacts/.`);
