import { deflateRawSync } from 'node:zlib';
import { readFile, writeFile, readdir, mkdir } from 'node:fs/promises';
import path from 'node:path';
const table = Array.from({ length: 256 }, (_, n) => { for (let i = 0; i < 8; i++) n = n & 1 ? 0xedb88320 ^ n >>> 1 : n >>> 1; return n >>> 0; });
function crc32(bytes) { let crc = 0xffffffff; for (const b of bytes) crc = table[(crc ^ b) & 255] ^ crc >>> 8; return (crc ^ 0xffffffff) >>> 0; }
export async function zipDirectory(directory, destination, prefix = '', excludes = new Set()) {
  async function walk(current) {
    const entries = await readdir(current, { withFileTypes: true });
    const out = [];
    for (const entry of entries.sort((a, b) => a.name.localeCompare(b.name))) {
      if (excludes.has(entry.name)) continue;
      const filename = path.join(current, entry.name);
      if (entry.isDirectory()) out.push(...await walk(filename));
      else if (entry.isFile()) out.push(filename);
    }
    return out;
  }
  const local = [], central = [];
  let offset = 0;
  const files = await walk(directory);
  for (const file of files) {
    const name = Buffer.from(prefix + path.relative(directory, file).split(path.sep).join('/'));
    const bytes = await readFile(file);
    const zipped = deflateRawSync(bytes, { level: 9 });
    const crc = crc32(bytes);
    const header = Buffer.alloc(30);
    header.writeUInt32LE(0x04034b50, 0); header.writeUInt16LE(20, 4); header.writeUInt16LE(0x800, 6);
    header.writeUInt16LE(8, 8); header.writeUInt16LE(0x5d45, 12); // Fixed valid DOS date, reproducible archives.
    header.writeUInt32LE(crc, 14); header.writeUInt32LE(zipped.length, 18); header.writeUInt32LE(bytes.length, 22); header.writeUInt16LE(name.length, 26);
    local.push(header, name, zipped);
    const dir = Buffer.alloc(46);
    dir.writeUInt32LE(0x02014b50, 0); dir.writeUInt16LE(20, 4); dir.writeUInt16LE(20, 6); dir.writeUInt16LE(0x800, 8);
    dir.writeUInt16LE(8, 10); dir.writeUInt16LE(0x5d45, 14); dir.writeUInt32LE(crc, 16);
    dir.writeUInt32LE(zipped.length, 20); dir.writeUInt32LE(bytes.length, 24); dir.writeUInt16LE(name.length, 28); dir.writeUInt32LE(offset, 42);
    central.push(dir, name); offset += header.length + name.length + zipped.length;
  }
  const centralSize = central.reduce((size, b) => size + b.length, 0);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0); end.writeUInt16LE(files.length, 8); end.writeUInt16LE(files.length, 10);
  end.writeUInt32LE(centralSize, 12); end.writeUInt32LE(offset, 16);
  await mkdir(path.dirname(destination), { recursive: true });
  await writeFile(destination, Buffer.concat([...local, ...central, end]));
}
