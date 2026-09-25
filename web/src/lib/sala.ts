// El contrato con conf-hub, visto desde el navegador, y la lógica pura de
// una pantalla de subtítulos. Todo lo que se puede probar sin navegador vive
// acá (tests/sala.test.ts); las islas sólo lo dibujan.

export type Idioma = "es" | "en";

export interface Sala {
  id: string;
  nombre: string;
  idioma: Idioma; // idioma de la charla
  backend: "gpu" | "cpu" | "gemini";
  demo: boolean;
  en_vivo: boolean;
  espectadores: number;
  glosario: string;
}

// Un evento `final` o `parcial` del SSE (hub: bus.go, Subtitulo).
export interface Subtitulo {
  id: string;
  tipo: "final" | "parcial";
  orig: string;
  idioma: Idioma;
  es: string;
  en: string;
  t_ini: number;
  t_fin: number;
  backend: string;
  lat_ms: number;
  seq: number;
}

// El evento `estado` que el hub manda al conectar y cada 15 s.
export interface EstadoSala {
  en_vivo: boolean;
  demo: boolean;
  backend: string; // el que la sala pide
  atendio: string; // el que atendió el último final (con la GPU caída, gemini)
  motor: "ok" | "degradado" | "caido";
  gpu: string;
  lat_p50_ms: number;
  espectadores: number;
}

export const NOMBRE_IDIOMA: Record<Idioma, string> = { es: "español", en: "english" };

// El texto de un subtítulo en el idioma que la persona eligió. Si el
// motor todavía no trajo la traducción (un parcial: sólo trae el original),
// devuelve "" y la pantalla no muestra nada en vez de mostrar el idioma
// equivocado.
export function textoEn(s: Subtitulo, idioma: Idioma): string {
  if (s.idioma === idioma) return s.orig;
  return idioma === "es" ? s.es : s.en;
}

export interface Pantalla {
  lineas: { id: string; texto: string; t_fin: number }[];
  provisional: string;
  ultimoSeq: number;
}

export const PANTALLA_VACIA: Pantalla = { lineas: [], provisional: "", ultimoSeq: 0 };
const MAX_GUARDADAS = 40;

/**
 * Aplica un evento a la pantalla. Reglas, cada una con su test:
 *  - un final agrega su línea UNA vez (si el mismo id vuelve a llegar, por
 *    un replay tras reconectar, no se duplica);
 *  - un final borra el provisional de su tramo o de uno anterior;
 *  - un parcial de un tramo cuyo final ya llegó no hace nada;
 *  - un final vacío en el idioma elegido no agrega una línea en blanco.
 */
export function aplicar(p: Pantalla, s: Subtitulo, idioma: Idioma): Pantalla {
  if (s.tipo === "parcial") {
    if (s.seq <= p.ultimoSeq) return p;
    return { ...p, provisional: textoEn(s, idioma) };
  }
  if (p.lineas.some((l) => l.id === s.id)) return p;
  const texto = textoEn(s, idioma).trim();
  const ultimoSeq = Math.max(p.ultimoSeq, s.seq);
  const provisional = "";
  if (!texto) return { ...p, provisional, ultimoSeq };
  const lineas = [...p.lineas, { id: s.id, texto, t_fin: s.t_fin }].slice(-MAX_GUARDADAS);
  return { lineas, provisional, ultimoSeq };
}

// "hace 3 min": para las líneas que llegan del replay cuando alguien entra a
// una sala. Sin esto, lo dicho hace media hora se lee como dicho ahora.
export function hace(msAtras: number): string {
  const s = Math.max(0, Math.round(msAtras / 1000));
  if (s < 60) return "ahora";
  const m = Math.round(s / 60);
  if (m < 60) return `hace ${m} min`;
  return `hace ${Math.round(m / 60)} h`;
}

export function segundos(ms: number): string {
  return `${(ms / 1000).toFixed(1).replace(".", ",")} s`;
}

// Parámetros de la URL, con lo que venga validado.
export function leerParametros(search: string): { sala: string; idioma: Idioma | null } {
  const q = new URLSearchParams(search);
  const sala = (q.get("s") ?? "").toLowerCase();
  const l = q.get("l");
  return {
    sala: /^[a-z0-9][a-z0-9-]{0,39}$/.test(sala) ? sala : "",
    idioma: l === "es" || l === "en" ? l : null,
  };
}

/**
 * Conecta al SSE de una sala. EventSource se reconecta solo y manda el
 * Last-Event-ID: tras un despliegue del hub (~3 s de corte) no se pierde
 * ninguna línea. Devuelve la función que cierra.
 */
export interface PreguntaT {
  id: string;
  texto: string;
  estado: "pendiente" | "publicada" | "respondida" | "descartada";
  creada: number;
  votos: number;
  votada: boolean;
  mia?: boolean;
}

export type Reaccion = "aplauso" | "fuego" | "duda" | "noseentiende";

/**
 * Conecta al SSE de una sala. EventSource se reconecta solo y manda el
 * Last-Event-ID: tras un despliegue del hub (~3 s de corte) no se pierde
 * ninguna línea. Devuelve la función que cierra.
 */
export function escuchar(
  sala: string,
  on: {
    subtitulo: (s: Subtitulo) => void;
    estado: (e: EstadoSala) => void;
    conexion: (abierta: boolean) => void;
    nivel?: (db: number) => void;
    reacciones?: (r: Partial<Record<Reaccion, number>>) => void;
    preguntas?: (p: PreguntaT[]) => void;
  },
): () => void {
  const es = new EventSource(`/api/salas/${encodeURIComponent(sala)}/subtitulos`);
  // Un evento roto no tumba la pantalla: se descarta y se sigue.
  const json = <T,>(f: ((x: T) => void) | undefined) => (ev: Event) => {
    if (!f) return;
    try {
      f(JSON.parse((ev as MessageEvent).data) as T);
    } catch {
      /* nada */
    }
  };
  es.addEventListener("final", json(on.subtitulo));
  es.addEventListener("parcial", json(on.subtitulo));
  es.addEventListener("estado", json(on.estado));
  es.addEventListener("nivel", json<{ db: number }>(on.nivel ? (x) => on.nivel!(x.db) : undefined));
  es.addEventListener("reacciones", json(on.reacciones));
  es.addEventListener("preguntas", json(on.preguntas));
  es.onopen = () => on.conexion(true);
  es.onerror = () => on.conexion(false);
  return () => es.close();
}

async function enviarJSON(url: string, cuerpo: unknown, metodo = "POST") {
  const r = await fetch(url, { method: metodo, headers: { "content-type": "application/json" },
    body: cuerpo === undefined ? undefined : JSON.stringify(cuerpo) });
  const datos = r.status === 204 ? {} : await r.json().catch(() => ({}));
  return { ok: r.ok, status: r.status, datos: datos as Record<string, unknown> };
}

export const reaccionar = (sala: string, id: Reaccion) => enviarJSON(`/api/salas/${sala}/reacciones`, { id });
export const preguntar = (sala: string, texto: string) => enviarJSON(`/api/salas/${sala}/preguntas`, { texto });
export const votar = (sala: string, pid: string) => enviarJSON(`/api/salas/${sala}/preguntas/${pid}/voto`, undefined);
export const sugerir = (sala: string, termino: string) => enviarJSON(`/api/salas/${sala}/sugerencias`, { termino });
export const moderar = (sala: string, pid: string, estado: string) =>
  enviarJSON(`/api/salas/${sala}/preguntas/${pid}`, { estado }, "PATCH");
export const resolverSugerencia = (sala: string, termino: string, aprobar: boolean) =>
  enviarJSON(`/api/salas/${sala}/sugerencias/resolver`, { termino, aprobar });

export async function preguntas(sala: string, todas = false): Promise<PreguntaT[]> {
  const r = await fetch(`/api/salas/${sala}/preguntas${todas ? "?todas=1" : ""}`);
  return r.ok ? ((await r.json()) as PreguntaT[]) : [];
}

// Mezcla la lista pública que llega por SSE con lo propio (mis votos y mis
// pendientes, que el evento no trae porque es igual para todos).
export function mezclarPreguntas(publicas: PreguntaT[], mias: PreguntaT[], votadas: Set<string>): PreguntaT[] {
  const pendientes = mias.filter((p) => p.estado === "pendiente" && !publicas.some((x) => x.id === p.id));
  return [...pendientes, ...publicas.map((p) => ({ ...p, votada: votadas.has(p.id) }))];
}

export async function salas(): Promise<Sala[]> {
  const r = await fetch("/api/salas", { headers: { accept: "application/json" } });
  if (!r.ok) throw new Error(`/api/salas → ${r.status}`);
  return (await r.json()) as Sala[];
}
