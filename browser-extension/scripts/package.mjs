import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { zipDirectory } from './zip.mjs';
import { mkdir, copyFile } from 'node:fs/promises';
const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
for (const target of ['chrome', 'firefox']) await zipDirectory(path.join(root, 'dist', target), path.join(root, 'artifacts', `localmeetassist-${target}-1.4.0.zip`));
await mkdir(path.join(root,'packages'),{recursive:true});
for (const target of ['chrome','firefox']) await copyFile(path.join(root,'artifacts',`localmeetassist-${target}-1.4.0.zip`),path.join(root,'packages',`localmeetassist-${target}-1.4.0.zip`));
await zipDirectory(root, path.join(root, 'artifacts', 'LocalMeetAssist_Browser_Extensions_1.4.0.zip'), 'localmeetassist-browser-extension/',
  new Set(['artifacts', 'node_modules', '.git', '.DS_Store', 'Thumbs.db']));
console.log('Created browser ZIPs and GitHub source ZIP in artifacts/.');
