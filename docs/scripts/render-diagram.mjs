// Renders the animated About diagram (SMIL SVG) into video-ready frames, by
// SEEKING the animation frame by frame (svg.setCurrentTime) — deterministic,
// not a screen capture. Meant to run inside the official Playwright image, with
// ffmpeg assembling the result in a second throwaway container — nothing is
// installed on the host:
//
//   cd docs
//   docker run --rm -v "$PWD:/work" -w /work mcr.microsoft.com/playwright:v1.61.1-noble \
//     sh -c "npm i --no-save playwright@1.61.1 >/dev/null 2>&1 && node scripts/render-diagram.mjs"
//   docker run --rm -v "$PWD:/work" -w /work linuxserver/ffmpeg \
//     -framerate 15 -i /work/.frames/f%03d.png -c:v libx264 -pix_fmt yuv420p -crf 20 \
//     -movflags +faststart /work/media/about-diagram.mp4
//
// (GIF and the hero PNG: see docs/media/README.md.)
import { chromium } from 'playwright';
import { mkdirSync } from 'node:fs';
import { resolve } from 'node:path';

const SVG = resolve('src/assets/about-diagram.svg');
const OUT = resolve('.frames');
const DUR = 16; // the SMIL loop length, seconds
const FPS = 15;
const SCALE = 2; // 900x511 → 1800x1022, crisp on retina feeds

mkdirSync(OUT, { recursive: true });
const browser = await chromium.launch();
const page = await browser.newPage({
  viewport: { width: 900, height: 511 },
  deviceScaleFactor: SCALE,
});
await page.goto('file://' + SVG);
await page.evaluate(() => document.documentElement.pauseAnimations());

const frames = DUR * FPS;
for (let i = 0; i < frames; i++) {
  await page.evaluate((t) => document.documentElement.setCurrentTime(t), i / FPS);
  await page.screenshot({ path: `${OUT}/f${String(i).padStart(3, '0')}.png` });
  if (i % 30 === 0) console.log(`frame ${i}/${frames}`);
}
await browser.close();
console.log(`${frames} frames → ${OUT}`);
