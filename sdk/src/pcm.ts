/**
 * PCM conversion, byte-for-byte compatible with the server's
 * `internal/voip/media/pcm.go`.
 *
 * The asymmetric scale (32768 down, 32767 up) is deliberate: it uses the full
 * negative range of int16 without letting +1.0 wrap to -32768. The server
 * divides by 32768 in both directions, which makes the round trip lossless
 * enough to be inaudible.
 */

export const float32ToInt16LE = (pcm: Float32Array): ArrayBuffer => {
  const view = new DataView(new ArrayBuffer(pcm.length * 2));
  for (let i = 0; i < pcm.length; i += 1) {
    let s = pcm[i];
    if (Number.isNaN(s)) s = 0;
    else if (s > 1) s = 1;
    else if (s < -1) s = -1;
    view.setInt16(i * 2, s < 0 ? Math.round(s * 32768) : Math.round(s * 32767), true);
  }
  return view.buffer;
};

/**
 * Tolerates any length: the server sends 960-sample frames (~60 ms) while we
 * send 320-sample ones (20 ms), and a trailing odd byte is dropped rather than
 * read past the end.
 */
export const int16LEToFloat32 = (buf: ArrayBuffer): Float32Array => {
  const view = new DataView(buf);
  const n = Math.floor(buf.byteLength / 2);
  const out = new Float32Array(n);
  for (let i = 0; i < n; i += 1) out[i] = view.getInt16(i * 2, true) / 32768;
  return out;
};
