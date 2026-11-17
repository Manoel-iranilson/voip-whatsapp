// ~12 updates per second is plenty for a level bar. Polling every animation
// frame meant two React re-renders per frame per call, which on iPhone kept
// the main thread busy enough to delay the audio frames it also relays.
const INTERVAL_MS = 80;

/** Polls an analyser's RMS level, in dBFS clamped to [-60, 0]. */
export const attachMeter = (analyser: AnalyserNode, onDb: (db: number) => void): (() => void) => {
  const data = new Float32Array(analyser.fftSize);
  let lastDb = Number.NaN;
  const t = setInterval(() => {
    analyser.getFloatTimeDomainData(data);
    let sum = 0;
    for (let i = 0; i < data.length; i += 1) sum += data[i] * data[i];
    const rms = Math.sqrt(sum / data.length);
    const db = Math.round(rms > 0 ? Math.max(-60, Math.min(0, 20 * Math.log10(rms))) : -60);
    // Skip the re-render when the bar would not move.
    if (db !== lastDb) {
      lastDb = db;
      onDb(db);
    }
  }, INTERVAL_MS);
  return () => clearInterval(t);
};
