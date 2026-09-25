import { useEffect, useRef, useState } from "react";
import {
  PliegoBarra, PliegoBoton, PliegoEnVivo, PliegoEstado, PliegoFilete, PliegoOnda, PliegoTitulo,
} from "@ahroi/foundation/pliego";
import { leerParametros } from "../lib/sala";
import { conexionMuerta, duracion, espera, recordada, recordar, Silencio } from "../lib/emision";

// La consola de una sala: /emitir/?s=<sala>#<token>.
//
// Pensada para el esquema real de un escenario: una mini PC al lado de la
// placa de audio, con el cable de 3,5 mm en la entrada, y alguien que
// entra por escritorio remoto sólo si algo se traba. La consola se
// recupera SOLA de todo lo que se puede recuperar:
//   - un corte de red o un despliegue del hub  → reconecta (latido cada 1 s;
//     5 s sin latido = muerta aunque el navegador no se entere);
//   - el cable o la placa que se desconecta    → vuelve a pedir la entrada
//     cada 2 s hasta que vuelva;
//   - el navegador que suspende el audio        → lo reanuda;
//   - un F5 o un reinicio de la mini PC         → vuelve a emitir sola;
//   - la pantalla que se duerme                 → la mantiene despierta.
// Y grita lo que no puede arreglar: un minuto sin sonido.
//
// El token viaja en el FRAGMENTO de la URL (#…): no sale del navegador, no
// queda en ningún log.

type Fase = "quieta" | "pidiendo" | "conectando" | "emitiendo" | "reconectando" | "sin-entrada" | "suspendida" | "error";
type Fuente = "microfono" | "pestana" | "archivo";

export default function Emitir() {
  const [{ sala }] = useState(() => leerParametros(location.search));
  const [token] = useState(() => location.hash.replace(/^#/, ""));
  const [fase, setFase] = useState<Fase>("quieta");
  const [error, setError] = useState("");
  const [db, setDb] = useState(-90);
  const [tramos, setTramos] = useState(0);
  const [desde, setDesde] = useState<number | null>(null);
  const [cortes, setCortes] = useState({ n: 0, ultimo: 0 });
  const [ahora, setAhora] = useState(() => Date.now());
  const [archivo, setArchivo] = useState<{ nombre: string; dura: number; va: number } | null>(null);
  const [repetir, setRepetir] = useState(true);
  const [auto, setAuto] = useState(false);
  const silencio = useRef(new Silencio());
  const cerrar = useRef<() => void>(() => {});
  const reanudar = useRef<() => void>(() => {});

  useEffect(() => {
    const t = setInterval(() => setAhora(Date.now()), 1000);
    return () => clearInterval(t);
  }, []);

  // Tras un F5 o un reinicio: si esta sala estaba emitiendo el micrófono,
  // vuelve a emitir sola. (La pestaña y el archivo piden una elección
  // humana: no se reanudan.)
  useEffect(() => {
    if (sala && token && recordada(sala) === "microfono") {
      setAuto(true);
      empezar("microfono");
    }
    return () => cerrar.current();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  async function empezar(fuente: Fuente, fichero?: File) {
    setError("");
    setFase("pidiendo");
    let parado = false;
    let ws: WebSocket | null = null;
    let listo = false;
    let intentos = 0;
    let ultimoMensaje = Date.now();
    let corteDesde: number | null = null;
    let stream: MediaStream | null = null;
    let entrada: AudioNode | null = null;
    let fatal = false;
    let soltarArchivo = () => {};
    let bloqueo: { release: () => Promise<void> } | null = null;
    const timers: number[] = [];

    const ctx = new AudioContext();
    await ctx.audioWorklet.addModule("/pcm-worklet.js");
    const nodo = new AudioWorkletNode(ctx, "pcm-16k");

    // ---- la entrada de audio ----------------------------------------------
    // true = entrada tomada; false = no está (se reintenta si es el
    // micrófono y el problema no es un permiso negado).
    async function tomarEntrada(): Promise<boolean> {
      try {
        entrada?.disconnect();
        if (fuente === "archivo") {
          if (!fichero) throw new Error("no elegiste un archivo");
          // A ritmo real, como si sonara en la sala.
          const buf = await ctx.decodeAudioData(await fichero.arrayBuffer());
          const src = ctx.createBufferSource();
          src.buffer = buf;
          src.loop = repetir;
          src.start();
          const t0 = ctx.currentTime;
          const reloj = window.setInterval(() => setArchivo({ nombre: fichero.name, dura: buf.duration,
            va: (ctx.currentTime - t0) % buf.duration }), 500);
          src.onended = () => { if (!repetir) cerrar.current(); };
          soltarArchivo = () => { clearInterval(reloj); try { src.stop(); } catch { /* ya paró */ } setArchivo(null); };
          entrada = src;
        } else {
          stream = fuente === "microfono"
            ? await navigator.mediaDevices.getUserMedia({ audio: { echoCancellation: false, noiseSuppression: false, autoGainControl: true } })
            : await navigator.mediaDevices.getDisplayMedia({ audio: true, video: true });
          const pista = stream.getAudioTracks()[0];
          if (!pista) throw new Error("la pestaña elegida no comparte audio");
          // El cable o la placa se desconectan: se vuelve a pedir la entrada.
          pista.onended = () => { if (!parado && fuente === "microfono") perderEntrada(); };
          entrada = ctx.createMediaStreamSource(stream);
        }
        entrada.connect(nodo);
        return true;
      } catch (e) {
        const permiso = (e as Error).name === "NotAllowedError";
        if (fuente === "microfono" && !permiso) return false;
        fatal = true;
        setFase("error");
        setError(fuente === "archivo"
          ? `No pude leer el archivo: ${(e as Error).message}. Probá con mp3, wav, ogg o m4a.`
          : permiso
            ? "El navegador no dio permiso para el micrófono. Aceptalo en el candado de la barra de direcciones."
            : `No pude tomar el audio: ${(e as Error).message}`);
        return false;
      }
    }

    function perderEntrada() {
      setFase("sin-entrada");
      stream?.getTracks().forEach((t) => t.stop());
      entrada?.disconnect();
      entrada = null;
      const reintentar = async () => {
        if (parado) return;
        if (await tomarEntrada()) setFase(listo ? "emitiendo" : "conectando");
        else if (!fatal) timers.push(window.setTimeout(reintentar, 2000));
      };
      timers.push(window.setTimeout(reintentar, 1000));
    }

    if (!(await tomarEntrada())) {
      if (fatal) {
        ctx.close();
        return;
      }
      // Arranque sin la placa enchufada: se queda esperándola.
      perderEntrada();
    }

    // ---- el navegador que suspende el audio -------------------------------
    // Sin un gesto del usuario, Chrome arranca el AudioContext suspendido (un
    // F5 remoto no es un gesto). Se intenta reanudar; si no se puede, la
    // consola lo dice con un botón grande, y cualquier tecla o toque sirve.
    reanudar.current = () => { ctx.resume().catch(() => {}); };
    const mirarAudio = () => {
      if (parado) return;
      if (ctx.state === "running") {
        setFase((f) => (f === "suspendida" ? (listo ? "emitiendo" : "conectando") : f));
        return;
      }
      ctx.resume().catch(() => {});
      setTimeout(() => { if (!parado && ctx.state !== "running") setFase("suspendida"); }, 300);
    };
    ctx.onstatechange = mirarAudio;
    const alTocar = () => { if (ctx.state !== "running") ctx.resume().catch(() => {}); };
    window.addEventListener("pointerdown", alTocar);
    window.addEventListener("keydown", alTocar);
    mirarAudio();

    // ---- la pantalla despierta -------------------------------------------------
    const despierta = async () => {
      try {
        const wl = (navigator as unknown as { wakeLock?: { request: (t: string) => Promise<{ release: () => Promise<void> }> } }).wakeLock;
        bloqueo = wl ? await wl.request("screen") : null;
      } catch { /* sin permiso: no pasa nada */ }
    };
    const alVolver = () => { if (document.visibilityState === "visible") { despierta(); mirarAudio(); } };
    document.addEventListener("visibilitychange", alVolver);
    despierta();

    // ---- el hub -----------------------------------------------------------------
    const conectar = () => {
      if (parado) return;
      setFase((f) => (f === "emitiendo" || f === "reconectando" ? "reconectando"
        : f === "sin-entrada" || f === "suspendida" ? f : "conectando"));
      const proto = location.protocol === "https:" ? "wss:" : "ws:";
      ws = new WebSocket(`${proto}//${location.host}/api/salas/${sala}/audio`);
      ws.binaryType = "arraybuffer";
      ultimoMensaje = Date.now();
      ws.onopen = () => ws?.send(JSON.stringify({ token }));
      ws.onmessage = (ev) => {
        ultimoMensaje = Date.now();
        try {
          const m = JSON.parse(ev.data as string);
          if (m.ok) {
            listo = true;
            intentos = 0;
            if (corteDesde !== null) {
              const dur = Date.now() - corteDesde;
              setCortes((c) => ({ n: c.n + 1, ultimo: dur }));
              corteDesde = null;
            }
            setDesde((d) => d ?? Date.now());
            setFase((f) => (f === "sin-entrada" || f === "suspendida" ? f : "emitiendo"));
          }
          if (typeof m.tramos === "number") setTramos(m.tramos);
        } catch { /* nada */ }
      };
      ws.onclose = (ev) => alCortarse(ev.code);
    };
    function alCortarse(codigo: number) {
      listo = false;
      if (parado) return;
      if (codigo === 1008) {
        setFase("error");
        setError("El hub rechazó el token de emisión de esta sala. Pedí el enlace de nuevo en el panel.");
        detener(false);
        return;
      }
      if (corteDesde === null) corteDesde = Date.now();
      setFase((f) => (f === "sin-entrada" || f === "suspendida" ? f : "reconectando"));
      timers.push(window.setTimeout(conectar, espera(intentos++)));
    }
    // Abandonar una conexión muerta SIN esperar su cierre: close() sobre un
    // socket cuyo otro lado no contesta deja el aviso de cierre colgado
    // (medido: con el hub congelado, la consola seguía diciendo "emitiendo"
    // y no mandaba nada). Se sueltan sus manejadores y se reintenta ya.
    function abandonar() {
      const viejo = ws;
      if (!viejo) return;
      viejo.onclose = null;
      viejo.onmessage = null;
      viejo.onopen = null;
      try { viejo.close(); } catch { /* ya estaba cerrado */ }
      ws = null;
      alCortarse(1006);
    }
    // Volvió la red: no esperar al próximo reintento.
    const alVolverRed = () => { if (!listo && ws) abandonar(); };
    window.addEventListener("online", alVolverRed);

    // El vigilante: una conexión que no late está muerta aunque el
    // navegador crea que sigue abierta (un wifi que se cae sin cerrar), y un
    // intento que no termina de conectar tampoco sirve (un hub congelado
    // acepta el TCP y nunca contesta). En los dos casos: cerrar, y el
    // onclose reintenta. ultimoMensaje se reinicia en cada intento.
    const vigilante = window.setInterval(() => {
      if (ws && ws.readyState !== WebSocket.CLOSED && conexionMuerta(ultimoMensaje, Date.now())) abandonar();
    }, 1000);

    nodo.port.onmessage = (ev: MessageEvent<{ pcm: ArrayBuffer; db: number }>) => {
      setDb(ev.data.db);
      silencio.current.registrar(ev.data.db, Date.now());
      if (listo && ws?.readyState === WebSocket.OPEN) ws.send(ev.data.pcm);
    };

    function detener(olvidar: boolean) {
      parado = true;
      timers.forEach(clearTimeout);
      clearInterval(vigilante);
      window.removeEventListener("online", alVolverRed);
      window.removeEventListener("pointerdown", alTocar);
      window.removeEventListener("keydown", alTocar);
      document.removeEventListener("visibilitychange", alVolver);
      bloqueo?.release().catch(() => {});
      ws?.close();
      soltarArchivo();
      stream?.getTracks().forEach((t) => t.stop());
      ctx.close();
      if (olvidar) recordar(sala, null);
    }
    cerrar.current = () => {
      detener(true);
      setFase("quieta");
      setDesde(null);
      setAuto(false);
    };
    recordar(sala, fuente === "microfono" ? "microfono" : null);
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
  const activa = fase !== "quieta" && fase !== "error";
  const callado = activa && silencio.current.alarma(ahora);
  const envivo = fase === "emitiendo" && !callado ? "vivo" : activa ? "quieto" : "sinver";
  return (
    <div className="lamina">
      <PliegoTitulo size="seccion" as="h1" sobre="consola de emisión">{sala}</PliegoTitulo>
      <div className="fila">
        <PliegoEnVivo estado={envivo} />
        <PliegoOnda nivel={activa ? db : -90} barras={20} label="Nivel de la entrada" />
      </div>

      <div role="status" aria-live="polite" data-fase={callado && fase === "emitiendo" ? "sin-sonido" : fase}
        style={{ display: "grid", gap: ".6rem", maxWidth: "40rem" }}>
        {fase === "emitiendo" && !callado && <PliegoEstado estado="bien">emitiendo</PliegoEstado>}
        {fase === "conectando" && <PliegoEstado estado="aviso">conectando con el hub…</PliegoEstado>}
        {fase === "reconectando" && <PliegoEstado estado="aviso">se cortó la conexión: reconectando sola…</PliegoEstado>}
        {fase === "pidiendo" && <PliegoEstado estado="aviso">esperando la entrada de audio…</PliegoEstado>}
        {fase === "sin-entrada" && <PliegoEstado estado="mal">no hay entrada de audio: revisá el cable o la placa. Reintento cada 2 s.</PliegoEstado>}
        {fase === "suspendida" && (
          <>
            <PliegoEstado estado="mal">el navegador pausó el audio</PliegoEstado>
            <div><PliegoBoton tone="rosa" cursor onClick={() => reanudar.current()}>tocar para reanudar</PliegoBoton></div>
          </>
        )}
        {callado && fase === "emitiendo" && (
          <PliegoEstado estado="mal">no llega sonido hace {duracion(silencio.current.hace(ahora))}: revisá el cable, la placa o el volumen</PliegoEstado>
        )}
        {fase === "error" && <PliegoEstado estado="mal">{error}</PliegoEstado>}
      </div>

      {!activa ? (
        <div className="fila">
          <PliegoBoton tone="rosa" cursor onClick={() => empezar("microfono")}>emitir el micrófono o la entrada de línea</PliegoBoton>
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

      <div style={{ display: "grid", gap: ".75rem", maxWidth: "40rem" }}>
        <PliegoBarra label="Nivel de entrada" value={nivel} max={60} readout={`${Math.round(db)} dBFS`} tone={nivel > 57 ? "rosa" : "tinta"} />
        <PliegoFilete label="tramos enviados al motor" readout={String(tramos)} />
        {desde && (
          <PliegoFilete label="emitiendo hace"
            readout={`${duracion(ahora - desde)} · ${cortes.n} ${cortes.n === 1 ? "corte" : "cortes"}${cortes.n ? `, el último de ${duracion(cortes.ultimo)}` : ""}`} />
        )}
        {archivo && (
          <PliegoBarra label={archivo.nombre} value={archivo.va} max={archivo.dura}
            readout={`${Math.floor(archivo.va / 60)}:${String(Math.floor(archivo.va % 60)).padStart(2, "0")} / ${Math.floor(archivo.dura / 60)}:${String(Math.floor(archivo.dura % 60)).padStart(2, "0")}`} />
        )}
        {auto && <p className="micro" style={{ margin: 0 }}>Reanudada sola: esta sala estaba emitiendo el micrófono.</p>}
      </div>

      <details>
        <summary className="micro" style={{ cursor: "pointer" }}>para una mini pc dedicada al escenario</summary>
        <div className="prosa" style={{ marginTop: ".8rem", display: "grid", gap: ".6rem" }}>
          <p style={{ margin: 0 }}>Enchufá la placa de audio y abrí este enlace una vez: aceptá el permiso del micrófono
            (queda recordado) y tocá «emitir el micrófono». Desde ahí la consola se recupera sola de cortes de red, de un
            despliegue del hub, de la placa que se desconecta y de un F5.</p>
          <p style={{ margin: 0 }}>Para que arranque sin tocar nada después de un reinicio, abrí Chrome en modo kiosco con el
            audio habilitado sin gesto:</p>
          <pre style={{ margin: 0, whiteSpace: "pre-wrap", fontSize: ".85rem" }}>google-chrome --kiosk --autoplay-policy=no-user-gesture-required "&lt;este enlace, con su #token&gt;"</pre>
        </div>
      </details>
    </div>
  );
}
