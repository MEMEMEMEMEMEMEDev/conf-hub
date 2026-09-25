// El procesador de audio de /emitir: toma el audio del micrófono o de la
// pestaña a la frecuencia que traiga (casi siempre 48 kHz), lo baja a
// 16 kHz mono con un promedio por ventana (antialias barato y suficiente
// para voz) y lo entrega en bloques de 100 ms como PCM s16le. Es el
// formato que el hub espera de punta a punta.
class PCM16k extends AudioWorkletProcessor {
  constructor() {
    super();
    this.razon = sampleRate / 16000;
    this.acc = 0; this.n = 0; this.fase = 0;
    this.bloque = new Int16Array(1600); this.i = 0;
    this.energia = 0;
  }
  process(entradas) {
    const canales = entradas[0];
    if (!canales || canales.length === 0) return true;
    const largo = canales[0].length;
    for (let k = 0; k < largo; k++) {
      let m = 0;
      for (let c = 0; c < canales.length; c++) m += canales[c][k];
      m /= canales.length;
      this.acc += m; this.n++; this.fase += 1;
      if (this.fase >= this.razon) {
        this.fase -= this.razon;
        const v = Math.max(-1, Math.min(1, this.acc / this.n));
        this.acc = 0; this.n = 0;
        this.energia += v * v;
        this.bloque[this.i++] = v < 0 ? v * 0x8000 : v * 0x7fff;
        if (this.i === this.bloque.length) {
          const rms = Math.sqrt(this.energia / this.bloque.length);
          this.port.postMessage({ pcm: this.bloque.buffer, db: rms > 1e-5 ? 20 * Math.log10(rms) : -100 }, [this.bloque.buffer]);
          this.bloque = new Int16Array(1600); this.i = 0; this.energia = 0;
        }
      }
    }
    return true;
  }
}
registerProcessor("pcm-16k", PCM16k);
