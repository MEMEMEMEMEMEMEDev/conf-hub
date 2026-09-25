import { useEffect, useRef, useState } from "react";
import { PliegoBarra, PliegoBoton, PliegoEstado, PliegoFilete, PliegoTitulo } from "@ahroi/foundation/pliego";
import { leerParametros } from "../lib/sala";

// La consola de una sala: toma el micrófono (o el audio de una pestaña, p.
// ej. el stream de la charla) y lo manda al hub por WebSocket como PCM
// 16 kHz. El token de emisión viaja en el FRAGMENTO de la URL (#…): el
// fragmento no sale del navegador, así que no queda en ningún log.
type Fase = "quieta" | "pidiendo" | "conectando" | "emitiendo" | "reconectando" | "error";

export default function Emitir() {
  const [{ sala }] = useState(() => leerParametros(location.search));
  const [token] = useState(() => location.hash.replace(/^#/, ""));
  const [fase, setFase] = useState<Fase>("quieta");
  const [error, setError] = useState("");
  const [db, setDb] = useState(-90);
  const [tramos, setTramos] = useState(0);
  const cerrar = useRef<() => void>(() => {});

  useEffect(() => () => cerrar.current(), []);

  const [archivo, setArchivo] = useState<{ nombre: string; dura: number; va: number } | null>(null);
  const [repetir, setRepetir] = useState(true);

  async function empezar(fuente: "microfono" | "pestana" | "archivo", fichero?: File) {
    setError("");
    setFase("pidiendo");
    const ctx = new AudioContext();
    await ctx.audioWorklet.addModule("/pcm-worklet.js");
    // Cada fuente termina en un nodo de audio; desde ahí el camino es el
    // mismo: el procesador lo baja a 16 kHz y lo manda al hub.
    let entrada: AudioNode;
    let soltar: () => void;
    try {
      if (fuente === "archivo") {
        if (!fichero) throw new Error("no elegiste un archivo");
        // El archivo se reproduce A RITMO REAL, como si sonara en la sala:
        // el hub y el motor no distinguen un archivo de un micrófono.
        const buf = await ctx.decodeAudioData(await fichero.arrayBuffer());
        const src = ctx.createBufferSource();
        src.buffer = buf;
        src.loop = repetir;
        src.start();
        const t0 = ctx.currentTime;
        const reloj = setInterval(() => setArchivo({ nombre: fichero.name, dura: buf.duration,
          va: (ctx.currentTime - t0) % buf.duration }), 500);
        src.onended = () => { if (!repetir) cerrar.current(); };
        entrada = src;
        soltar = () => { clearInterval(reloj); try { src.stop(); } catch { /* ya paró */ } setArchivo(null); };
      } else {
        const stream = fuente === "microfono"
          ? await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: false, noiseSuppression: false, autoGainControl: true } })
          : await navigator.mediaDevices.getDisplayMedia({ audio: true, video: true });
        if (stream.getAudioTracks().length === 0) throw new Error("la pestaña elegida no comparte audio");
        entrada = ctx.createMediaStreamSource(stream);
        soltar = () => stream.getTracks().forEach((t) => t.stop());
      }
    } catch (e) {
      ctx.close();
      setFase("error");
      setError(fuente === "archivo"
        ? `No pude leer el archivo: ${(e as Error).message}. Probá con mp3, wav, ogg o m4a.`
        : `No pude tomar el audio: ${(e as Error).message}`);
      return;
    }
    const nodo = new AudioWorkletNode(ctx, "pcm-16k");
    entrada.connect(nodo);

    let ws: WebSocket | null = null;
    let listo = false;
    let parado = false;
    let espera = 500;
    const conectar = () => {
      setFase((f) => (f === "emitiendo" ? "reconectando" : "conectando"));
      const proto = location.protocol === "https:" ? "wss:" : "ws:";
      ws = new WebSocket(`${proto}//${location.host}/api/salas/${sala}/audio`);
      ws.binaryType = "arraybuffer";
      ws.onopen = () => ws?.send(JSON.stringify({ token }));
      ws.onmessage = (ev) => {
        try {
          const m = JSON.parse(ev.data as string);
          if (m.ok) {
            listo = true;
            espera = 500;
            setFase("emitiendo");
          }
          if (typeof m.tramos === "number") setTramos(m.tramos);
        } catch { /* nada */ }
      };
      ws.onclose = (ev) => {
        listo = false;
        if (parado) return;
        if (ev.code === 1008) {
          setFase("error");
          setError("El hub rechazó el token de emisión de esta sala. Pedí el enlace de nuevo en el panel.");
          detener();
          return;
        }
        // Un despliegue del hub corta unos segundos: se reintenta solo.
        setTimeout(conectar, espera);
        espera = Math.min(espera * 2, 8000);
      };
    };
    nodo.port.onmessage = (ev: MessageEvent<{ pcm: ArrayBuffer; db: number }>) => {
      setDb(ev.data.db);
      if (listo && ws?.readyState === WebSocket.OPEN) ws.send(ev.data.pcm);
    };
    const detener = () => {
      parado = true;
      ws?.close();
      soltar();
      ctx.close();
    };
    cerrar.current = () => {
      detener();
      setFase("quieta");
    };
    conectar();
  }

  if (!sala || !token) {
    return (
      <div className="lamina">
        <PliegoTitulo size="seccion" as="h1">falta el enlace de la sala</PliegoTitulo>
        <p className="prosa">Esta página se abre con el enlace de emisión que da el panel de producción
          (lleva la sala y su token). Pedíselo a quien opera el evento.</p>
      </div>
    );
  }
  const nivel = Math.max(0, Math.min(60, db + 60));
  const activa = fase === "emitiendo" || fase === "reconectando" || fase === "conectando";
  return (
    <div className="lamina">
      <PliegoTitulo size="seccion" as="h1" sobre="consola de emisión">{sala}</PliegoTitulo>
      <p className="prosa">Todo lo que entre por acá se transcribe y se traduce en vivo para la audiencia de la sala.
        Mantené esta pestaña abierta durante la charla. Un archivo se reproduce a ritmo real, como si sonara en la sala:
        sirve para probar con una charla grabada.</p>
      {!activa ? (
        <div className="fila">
          <PliegoBoton tone="rosa" cursor onClick={() => empezar("microfono")}>emitir el micrófono</PliegoBoton>
          <PliegoBoton variant="outline" onClick={() => empezar("pestana")}>emitir el audio de una pestaña</PliegoBoton>
          <label className="micro" style={{ display: "inline-flex", alignItems: "center", gap: ".5rem", cursor: "pointer",
            border: "1px solid var(--pliego-tinta)", padding: ".7rem 1rem", minHeight: "2.75rem", background: "var(--pliego-hoja)" }}>
            emitir un archivo de audio
            <input type="file" accept="audio/*,video/*" className="sr"
              onChange={(e) => { const f = e.target.files?.[0]; if (f) empezar("archivo", f); e.target.value = ""; }} />
          </label>
          <label className="micro" style={{ display: "inline-flex", alignItems: "center", gap: ".4rem" }}>
            <input type="checkbox" checked={repetir} onChange={(e) => setRepetir(e.target.checked)} /> repetir el archivo
          </label>
        </div>
      ) : (
        <div className="fila">
          <PliegoBoton variant="outline" onClick={() => cerrar.current()}>detener</PliegoBoton>
        </div>
      )}
      <div style={{ display: "grid", gap: ".75rem", maxWidth: "36rem" }}>
        {fase === "emitiendo" && <PliegoEstado estado="bien">emitiendo</PliegoEstado>}
        {fase === "conectando" && <PliegoEstado estado="aviso">conectando con el hub…</PliegoEstado>}
        {fase === "reconectando" && <PliegoEstado estado="aviso">se cortó: reconectando…</PliegoEstado>}
        {fase === "pidiendo" && <PliegoEstado estado="aviso">esperando el permiso del navegador…</PliegoEstado>}
        {fase === "error" && <PliegoEstado estado="mal">{error}</PliegoEstado>}
        <PliegoBarra label="Nivel de entrada" value={nivel} max={60} readout={`${Math.round(db)} dBFS`} tone={nivel > 57 ? "rosa" : "tinta"} />
        <PliegoFilete label="tramos enviados al motor" readout={String(tramos)} />
        {archivo && (
          <PliegoBarra label={archivo.nombre} value={archivo.va} max={archivo.dura}
            readout={`${Math.floor(archivo.va / 60)}:${String(Math.floor(archivo.va % 60)).padStart(2, "0")} / ${Math.floor(archivo.dura / 60)}:${String(Math.floor(archivo.dura % 60)).padStart(2, "0")}`} />
        )}
      </div>
    </div>
  );
}
