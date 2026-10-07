// Faithful port of LocalMeet's Go trayIcon(recording bool). The 64px PNG is pixel-identical.
// Browser assets use PNG directly; the Windows-only ICO container is unnecessary here.
import { deflateSync } from 'node:zlib';
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';

function pixels(recording) {
  const rgba = Buffer.alloc(64 * 64 * 4);
  const set = (x, y, c) => rgba.set(c, (y * 64 + x) * 4);
  const blue = [35, 101, 165, 255], white = [255, 255, 255, 255];
  for (let y = 4; y < 60; y++) for (let x = 4; x < 60; x++) {
    if ((x - 32) ** 2 + (y - 32) ** 2 <= 28 ** 2) set(x, y, blue);
  }
  for (const [left, top, right, bottom] of [[17, 18, 22, 43], [21, 38, 31, 43], [34, 20, 39, 43], [48, 20, 53, 43]]) {
    for (let y = top; y < bottom; y++) for (let x = left; x < right; x++) set(x, y, white);
  }
  for (let i = 0; i < 8; i++) { set(39 + i, 22 + i, white); set(47 - i, 22 + i, white); }
  if (recording) for (let y = 38; y < 64; y++) for (let x = 38; x < 64; x++) {
    if ((x - 51) ** 2 + (y - 51) ** 2 <= 12 ** 2) set(x, y, [224, 42, 42, 255]);
  }
  return rgba;
}

// Area resampling in premultiplied alpha avoids dark edges at toolbar sizes.
function scale(source, size) {
  if (size === 64) return source;
  const out = Buffer.alloc(size * size * 4), ratio = 64 / size;
  for (let y = 0; y < size; y++) for (let x = 0; x < size; x++) {
    const totals = [0, 0, 0, 0], left = x * ratio, top = y * ratio;
    for (let sy = Math.floor(top); sy < Math.ceil(top + ratio); sy++) {
      for (let sx = Math.floor(left); sx < Math.ceil(left + ratio); sx++) {
        const weight = (Math.min(sx + 1, left + ratio) - Math.max(sx, left)) *
          (Math.min(sy + 1, top + ratio) - Math.max(sy, top));
        const offset = (sy * 64 + sx) * 4, alpha = source[offset + 3];
        totals[3] += alpha * weight;
        for (let c = 0; c < 3; c++) totals[c] += source[offset + c] * alpha * weight;
      }
    }
    const offset = (y * size + x) * 4;
    for (let c = 0; c < 3; c++) out[offset + c] = totals[3] ? Math.round(totals[c] / totals[3]) : 0;
    out[offset + 3] = Math.round(totals[3] / (ratio * ratio));
  }
  return out;
}
function chunk(type, data) {
  const content = Buffer.concat([Buffer.from(type), data]);
  let crc = 0xffffffff;
  for (const b of content) {
    crc ^= b;
    for (let i = 0; i < 8; i++) crc = (crc >>> 1) ^ ((crc & 1) ? 0xedb88320 : 0);
  }
  const result = Buffer.alloc(data.length + 12);
  result.writeUInt32BE(data.length, 0); content.copy(result, 4);
  result.writeUInt32BE((crc ^ 0xffffffff) >>> 0, result.length - 4);
  return result;
}
function png(rgba, size) {
  const header = Buffer.alloc(13);
  header.writeUInt32BE(size, 0); header.writeUInt32BE(size, 4); header[8] = 8; header[9] = 6;
  const rows = Buffer.alloc(size * (size * 4 + 1));
  for (let y = 0; y < size; y++) rgba.copy(rows, y * (size * 4 + 1) + 1, y * size * 4, (y + 1) * size * 4);
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', header),
    chunk('IDAT', deflateSync(rows)), chunk('IEND', Buffer.alloc(0))]);
}
export async function generateIcons(root) {
  for (const recording of [false, true]) {
    const folder = path.join(root, 'src', 'icons', ...(recording ? ['recording'] : []));
    await mkdir(folder, { recursive: true });
    const source = pixels(recording);
    for (const size of [16, 32, 48, 64, 128]) await writeFile(path.join(folder, `${size}.png`), png(scale(source, size), size));
  }
}
