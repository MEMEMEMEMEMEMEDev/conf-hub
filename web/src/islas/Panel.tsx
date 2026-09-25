import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import {
  idsPestana, PliegoAviso, PliegoBoton, PliegoCampo, PliegoEnVivo, PliegoEstado, PliegoFilete, PliegoOnda,
  PliegoPestanas, PliegoPregunta, PliegoTabla, PliegoTitulo,
} from "@ahroi/foundation/pliego";
import type { PliegoEstadoTipo, PliegoTablaFila } from "@ahroi/foundation/pliego";
import { hace, moderar, resolverSugerencia, segundos } from "../lib/sala";
import type { Idioma, PreguntaT, Sala } from "../lib/sala";

interface Vivo {
  en_vivo: boolean; emisores: number; hace_audio_s: number; nivel: number; tramos: number;
  lat_p50_ms: number; lat_p95_ms: number; lat_muestras: number; saltados: number; errores: number;
  ultimo_error?: string; hace_error_s?: number; hace_final_s: number; atendio: string;
}
interface Interaccion { no_se_entiende_2min: number; preguntas_pendientes: number; sugerencias: number }
interface Fila extends Sala { vivo: Vivo; interaccion: Interaccion }
interface Motor { vivo?: boolean; gpu?: string; backend?: string; cola?: number; hace_s?: number; saltados?: number; errores?: number; latido?: number }
interface Estado { motor: Motor; salas: Fila[]; max_salas: number }
interface Bandeja {
  preguntas: { sala: string; nombre_sala: string; pregunta: PreguntaT }[];
  sugerencias: { sala: string; nombre_sala: string; termino: string; veces: number; creada: number }[];
}

const BACKENDS = ["gpu", "gemini", "cpu"] as const;
const PREFIJO = "panel";
const otro = (i: Idioma): Idioma => (i === "en" ? "es" : "en");

// El estado de una sala en UNA marca, con su por qué en palabras. El orden
// de las preguntas importa: lo que no se pudo mirar primero (sin motor, no
// hay nada que afirmar), después lo que está mal, después lo que avisa.
function estadoDe(f: Fila, m: Motor): [PliegoEstadoTipo, string] {
  if (!m.vivo) return ["sinver", "sin latido del motor"];
  // Tres personas diciendo "no se entiende" en 2 minutos es la alarma más
  // directa que hay: la da la audiencia, que es para quien existe esto.
  if (f.interaccion?.no_se_entiende_2min >= 3) return ["mal", `${f.interaccion.no_se_entiende_2min} «no se entiende» en 2 min`];
  // El error del hub ya viene en castellano y se borra solo cuando la sala
  // vuelve a dar subtítulos: si está, es de AHORA.
  if (f.vivo.ultimo_error) return ["mal", f.vivo.ultimo_error.replace(/^No salió el subtítulo: /, "sin subtítulo: ").slice(0, 70)];
  if (!f.vivo.en_vivo) {
    if (f.demo) return ["aviso", "demo: suena con audiencia"];
    return f.vivo.hace_audio_s < 0 ? ["aviso", "nunca recibió audio"] : ["aviso", `sin audio hace ${Math.round(f.vivo.hace_audio_s)} s`];
  }
  if (f.vivo.saltados > 0) return ["aviso", `en vivo · ${f.vivo.saltados} saltados`];
  return ["bien", "en vivo"];
}

const enlace = { all: "unset", cursor: "pointer", textDecoration: "underline", color: "var(--pliego-rosa-tinta)" } as const;

export default function Panel() {
  const [sesion, setSesion] = useState<"?" | "no" | "si">("?");
  const [usuario, setUsuario] = useState("");
  const [password, setPassword] = useState("");
  const [errorLogin, setErrorLogin] = useState("");
  const [estado, setEstado] = useState<Estado | null>(null);
  const [bandeja, setBandeja] = useState<Bandeja>({ preguntas: [], sugerencias: [] });
  const [sinDatos, setSinDatos] = useState(false);
  const [pestana, setPestana] = useState("salas");
  const [abierta, setAbierta] = useState<string | null>(null);
  const [creando, setCreando] = useState(false);
  const [nueva, setNueva] = useState({ nombre: "", idioma: "en" as Idioma, backend: "gpu" });
  const [errorNueva, setErrorNueva] = useState("");
  const [enlaces, setEnlaces] = useState<Record<string, string>>({});
  const [cambio, setCambio] = useState<{ sala: Fila; backend: string } | null>(null);
  const [glosario, setGlosario] = useState<{ sala: string; texto: string; error: string } | null>(null);

  const refrescar = useCallback(async () => {
    const r = await fetch("/api/estado").catch(() => null);
    if (!r) return setSinDatos(true);
    if (r.status === 401) return setSesion("no");
    if (!r.ok) return setSinDatos(true);
    setEstado(await r.json());
    setSinDatos(false);
    setSesion("si");
    const b = await fetch("/api/bandeja").catch(() => null);
    if (b?.ok) setBandeja(await b.json());
  }, []);

  useEffect(() => {
    refrescar();
    const t = setInterval(() => sesion === "si" && refrescar(), 3000);
    return () => clearInterval(t);
  }, [refrescar, sesion]);

  async function entrar(e: FormEvent) {
    e.preventDefault();
    setErrorLogin("");
    const r = await fetch("/api/sesion", { method: "POST", headers: { "content-type": "application/json" },
      body: JSON.stringify({ usuario, password }) });
    if (!r.ok) return setErrorLogin((await r.json().catch(() => ({}))).mensaje ?? `Error ${r.status}`);
    setPassword("");
    refrescar();
  }

  async function crear(e: FormEvent) {
    e.preventDefault();
    setErrorNueva("");
    const r = await fetch("/api/salas", { method: "POST", headers: { "content-type": "application/json" },
      body: JSON.stringify(nueva) });
    const d = await r.json().catch(() => ({}));
    if (!r.ok) return setErrorNueva(d.mensaje ?? `Error ${r.status}`);
    setEnlaces((x) => ({ ...x, [d.sala.id]: `${location.origin}/emitir/?s=${d.sala.id}#${d.token}` }));
    setNueva({ ...nueva, nombre: "" });
    setCreando(false);
    setAbierta(d.sala.id);
    refrescar();
  }

  async function pedirEnlace(id: string) {
    const r = await fetch(`/api/salas/${id}/token`);
    if (!r.ok) return;
    const { token } = await r.json();
    setEnlaces((x) => ({ ...x, [id]: `${location.origin}/emitir/?s=${id}#${token}` }));
  }

  async function aplicarCambio() {
    if (!cambio) return;
    await fetch(`/api/salas/${cambio.sala.id}`, { method: "PATCH", headers: { "content-type": "application/json" },
      body: JSON.stringify({ backend: cambio.backend }) });
    setCambio(null);
    refrescar();
  }

  async function guardarGlosario(e: FormEvent) {
    e.preventDefault();
    if (!glosario) return;
    const r = await fetch(`/api/salas/${glosario.sala}`, { method: "PATCH",
      headers: { "content-type": "application/json" }, body: JSON.stringify({ glosario: glosario.texto }) });
    if (!r.ok) {
      const d = await r.json().catch(() => ({}));
      return setGlosario({ ...glosario, error: d.mensaje ?? `Error ${r.status}` });
    }
    setGlosario(null);
    refrescar();
  }

  async function salir() {
    await fetch("/api/sesion", { method: "DELETE" });
    setSesion("no");
    setEstado(null);
  }

  if (sesion === "?") return <div className="lamina"><p className="micro">Revisando la sesión…</p></div>;
  if (sesion === "no") {
    return (
      <div className="lamina" style={{ maxWidth: "30rem" }}>
        <PliegoTitulo size="seccion" as="h1" sobre="producción">panel</PliegoTitulo>
        <form onSubmit={entrar} style={{ display: "grid", gap: "1rem" }}>
          <PliegoCampo etiqueta="Usuario" autoComplete="username" value={usuario} onChange={(e) => setUsuario(e.target.value)} required />
          <PliegoCampo etiqueta="Clave" type="password" autoComplete="current-password" value={password}
            onChange={(e) => setPassword(e.target.value)} error={errorLogin || undefined} required />
          <div><PliegoBoton type="submit" tone="rosa" cursor>entrar</PliegoBoton></div>
        </form>
      </div>
    );
  }

  const m = estado?.motor ?? {};
  const salas = estado?.salas ?? [];
  const motorEstado: [PliegoEstadoTipo, string] = !m.latido
    ? ["sinver", "el motor nunca latió"]
    : !m.vivo ? ["mal", `motor sin latido hace ${m.hace_s} s`]
    : m.gpu === "caida" ? ["mal", `GPU caída: las salas van por ${m.backend}`]
    : (m.hace_s ?? 0) > 15 ? ["aviso", `latido hace ${m.hace_s} s: ¿se está desplegando?`]
    : ["bien", m.gpu === "ok" ? "motor en GPU" : `motor sin GPU (${m.backend})`];

  const pendientes = bandeja.preguntas.length;
  const filas: PliegoTablaFila[] = salas.map((f) => {
    const [tipo, dicho] = estadoDe(f, m);
    const ne = f.interaccion?.no_se_entiende_2min ?? 0;
    const pq = f.interaccion?.preguntas_pendientes ?? 0;
    return {
      id: f.id,
      marcada: tipo === "mal",
      celdas: {
        sala: (
          <span style={{ display: "grid", gap: ".3rem" }}>
            <button type="button" onClick={() => setAbierta(abierta === f.id ? null : f.id)} aria-expanded={abierta === f.id}
              style={{ ...enlace, color: "var(--pliego-tinta)", fontWeight: 700 }}>
              {abierta === f.id ? "▾" : "▸"} {f.nombre}
            </button>
            <span className="fila" style={{ gap: ".5rem" }}>
              <PliegoEnVivo estado={!m.vivo ? "sinver" : f.vivo.en_vivo ? "vivo" : "quieto"} grabado={f.demo} />
              <PliegoOnda nivel={f.vivo.en_vivo ? f.vivo.nivel : -90} barras={8} />
            </span>
          </span>
        ),
        estado: <PliegoEstado estado={tipo}>{dicho}</PliegoEstado>,
        motor: f.vivo.atendio && f.vivo.atendio !== f.backend
          ? <PliegoEstado estado="aviso">{f.backend} → {f.vivo.atendio}</PliegoEstado>
          : <span className="micro">{f.backend}</span>,
        proceso: f.vivo.lat_muestras ? `${segundos(f.vivo.lat_p50_ms)} / ${segundos(f.vivo.lat_p95_ms)}` : "—",
        alarmas: (ne || pq) ? (
          <span style={{ display: "grid", gap: ".15rem", fontSize: ".85rem" }}>
            {ne > 0 && <span>! {ne} no se entiende</span>}
            {pq > 0 && <button type="button" style={enlace} onClick={() => setPestana("bandeja")}>? {pq} por moderar</button>}
          </span>
        ) : <span className="micro">—</span>,
        publico: f.espectadores,
      },
    };
  });
  const f = salas.find((x) => x.id === abierta);

  return (
    <div className="lamina" style={{ gap: "1.5rem" }}>
      <header className="fila" style={{ justifyContent: "space-between", alignItems: "end" }}>
        <PliegoTitulo size="seccion" as="h1" sobre="producción">panel</PliegoTitulo>
        <div className="fila">
          <PliegoEstado estado={motorEstado[0]}>{motorEstado[1]}</PliegoEstado>
          <PliegoBoton variant="outline" size="sm" onClick={salir}>salir</PliegoBoton>
        </div>
      </header>
      <PliegoFilete label="motor" readout={`cola ${m.cola ?? "—"} · saltados ${m.saltados ?? "—"} · errores ${m.errores ?? "—"} · tope ${estado?.max_salas ?? "—"} salas`} />
      {sinDatos && <PliegoEstado estado="sinver">No pude leer el estado. Reintento en 3 s.</PliegoEstado>}

      <PliegoPestanas etiqueta="Secciones del panel" prefijo={PREFIJO} activa={pestana} onCambio={setPestana}
        pestanas={[
          { id: "salas", nombre: "salas", cuenta: salas.length },
          { id: "bandeja", nombre: "bandeja", cuenta: pendientes, urgente: pendientes > 0 },
          { id: "glosario", nombre: "glosario", cuenta: bandeja.sugerencias.length, urgente: bandeja.sugerencias.length > 0 },
        ]} />

      {/* ---- salas ---- */}
      <section role="tabpanel" id={idsPestana("salas", PREFIJO).panel} aria-labelledby={idsPestana("salas", PREFIJO).pestana}
        hidden={pestana !== "salas"} style={{ display: pestana === "salas" ? "grid" : "none", gap: "1.25rem" }}>
        <PliegoTabla titulo="Salas" columnas={[
          { clave: "sala", titulo: "Sala" }, { clave: "estado", titulo: "Estado" }, { clave: "motor", titulo: "Motor" },
          { clave: "proceso", titulo: "Proceso p50 / p95", alinear: "der" }, { clave: "alarmas", titulo: "Audiencia" },
          { clave: "publico", titulo: "Mirando", alinear: "der" },
        ]} filas={filas} vacio="Todavía no hay salas." />

        {f && (
          <article aria-label={`Detalle de ${f.nombre}`} style={{ display: "grid", gap: "1rem", background: "var(--pliego-hoja)", padding: "1.25rem",
            borderLeft: "6px solid var(--pliego-rosa)" }}>
            <PliegoFilete label={f.nombre} readout={`${f.id} · la charla es en ${f.idioma === "en" ? "inglés" : "español"}`} />
            <div className="fila">
              <span className="micro">motor</span>
              {BACKENDS.map((b) => (
                <PliegoBoton key={b} size="sm" variant={f.backend === b ? "solid" : "outline"} aria-pressed={f.backend === b}
                  onClick={() => f.backend !== b && setCambio({ sala: f, backend: b })}>{b}</PliegoBoton>
              ))}
            </div>
            <p className="prosa" style={{ fontSize: ".95rem" }}>
              {f.vivo.tramos} tramos · {f.vivo.saltados} saltados · {f.vivo.errores} errores
              {f.vivo.ultimo_error ? ` · hace ${Math.round(f.vivo.hace_error_s ?? 0)} s: ${f.vivo.ultimo_error}` : ""}
              {f.glosario ? ` · glosario: ${f.glosario}` : " · sin glosario"}
            </p>
            <nav aria-label={`Enlaces de ${f.nombre}`} className="fila micro" style={{ gap: ".5rem 1rem" }}>
              <a href={`/sala/?s=${f.id}&l=${otro(f.idioma)}`}>audiencia</a>
              <a href={`/orador/?s=${f.id}`}>vista del orador</a>
              <a href={`/cartel/?s=${f.id}&l=${otro(f.idioma)}`}>cartel del proyector</a>
              <a href={`/obs/?s=${f.id}&l=${otro(f.idioma)}`}>overlay obs</a>
              {(["srt", "vtt", "txt"] as const).map((x) => (
                <a key={x} href={`/api/salas/${f.id}/export?formato=${x}&idioma=${otro(f.idioma)}`}>{x} ({otro(f.idioma)})</a>
              ))}
              {!f.demo && (enlaces[f.id]
                ? <a href={enlaces[f.id]}>emitir (enlace con token)</a>
                : <button type="button" className="micro" style={enlace} onClick={() => pedirEnlace(f.id)}>pedir enlace de emisión</button>)}
            </nav>
            {enlaces[f.id] && (
              <p className="prosa" style={{ fontSize: ".9rem" }}>El enlace de emisión lleva el token de la sala después del <code>#</code>:
                quien lo tenga puede emitir en esa sala. Compartilo sólo con quien opera el sonido.</p>
            )}
          </article>
        )}

        {!creando ? (
          <div><PliegoBoton tone="rosa" cursor onClick={() => setCreando(true)}>sala nueva</PliegoBoton></div>
        ) : (
          <form onSubmit={crear} style={{ display: "grid", gap: "1rem", maxWidth: "36rem" }}>
            <PliegoFilete label="sala nueva" />
            <PliegoCampo etiqueta="Nombre" placeholder="Auditorio principal" value={nueva.nombre} required maxLength={80} autoFocus
              onChange={(e) => setNueva({ ...nueva, nombre: e.target.value })} error={errorNueva || undefined}
              ayuda="Lo ve la audiencia al elegir sala." />
            <div className="fila">
              <span className="micro">la charla es en</span>
              {(["en", "es"] as Idioma[]).map((l) => (
                <PliegoBoton key={l} type="button" size="sm" variant={nueva.idioma === l ? "solid" : "outline"}
                  aria-pressed={nueva.idioma === l} onClick={() => setNueva({ ...nueva, idioma: l })}>{l === "en" ? "inglés" : "español"}</PliegoBoton>
              ))}
            </div>
            <div className="fila">
              <span className="micro">motor</span>
              {BACKENDS.map((b) => (
                <PliegoBoton key={b} type="button" size="sm" variant={nueva.backend === b ? "solid" : "outline"}
                  aria-pressed={nueva.backend === b} onClick={() => setNueva({ ...nueva, backend: b })}>{b}</PliegoBoton>
              ))}
            </div>
            <div className="fila">
              <PliegoBoton type="submit" tone="rosa" cursor>crear sala</PliegoBoton>
              <PliegoBoton type="button" variant="ghost" onClick={() => setCreando(false)}>cancelar</PliegoBoton>
            </div>
          </form>
        )}
      </section>

      {/* ---- bandeja ---- */}
      <section role="tabpanel" id={idsPestana("bandeja", PREFIJO).panel} aria-labelledby={idsPestana("bandeja", PREFIJO).pestana}
        hidden={pestana !== "bandeja"} style={{ display: pestana === "bandeja" ? "grid" : "none", gap: ".75rem" }}>
        <p className="prosa">Las preguntas de todas las salas que esperan moderación, la más vieja primero. Publicada, la sala
          la ve y la vota; el orador la ve en su vista.</p>
        {pendientes === 0 && <PliegoEstado estado="bien">Nada esperando.</PliegoEstado>}
        <div>
          {bandeja.preguntas.map(({ sala, nombre_sala, pregunta: p }) => (
            <div key={p.id}>
              <p className="micro" style={{ margin: ".75rem 0 .25rem" }}>{nombre_sala}</p>
              <PliegoPregunta texto={p.texto} votos={p.votos} estado="pendiente" cuando={hace(Date.now() - p.creada)}
                acciones={<>
                  <PliegoBoton size="sm" tone="rosa" onClick={async () => { await moderar(sala, p.id, "publicada"); refrescar(); }}>publicar</PliegoBoton>
                  <PliegoBoton size="sm" variant="outline" onClick={async () => { await moderar(sala, p.id, "descartada"); refrescar(); }}>descartar</PliegoBoton>
                </>} />
            </div>
          ))}
        </div>
      </section>

      {/* ---- glosario ---- */}
      <section role="tabpanel" id={idsPestana("glosario", PREFIJO).panel} aria-labelledby={idsPestana("glosario", PREFIJO).pestana}
        hidden={pestana !== "glosario"} style={{ display: pestana === "glosario" ? "grid" : "none", gap: "1.25rem" }}>
        <p className="prosa">Nombres propios y términos por sala: Whisper los busca en el audio. Sirven para nombres que suenan
          raro; con muchos, los mete donde no van. Hasta 20 por sala.</p>
        {bandeja.sugerencias.length > 0 && (
          <div style={{ display: "grid", gap: ".6rem" }}>
            <PliegoFilete label="sugerencias de la audiencia" readout={String(bandeja.sugerencias.length)} />
            {bandeja.sugerencias.map((s) => (
              <div key={s.sala + s.termino} className="fila">
                <strong>{s.termino}</strong>
                <span className="micro">{s.nombre_sala} · {s.veces} {s.veces === 1 ? "vez" : "veces"}</span>
                <PliegoBoton size="sm" tone="rosa" onClick={async () => { await resolverSugerencia(s.sala, s.termino, true); refrescar(); }}>al glosario</PliegoBoton>
                <PliegoBoton size="sm" variant="outline" onClick={async () => { await resolverSugerencia(s.sala, s.termino, false); refrescar(); }}>descartar</PliegoBoton>
              </div>
            ))}
          </div>
        )}
        <PliegoFilete label="glosario por sala" />
        {salas.map((s) => (
          glosario?.sala === s.id ? (
            <form key={s.id} onSubmit={guardarGlosario} style={{ display: "grid", gap: ".6rem", maxWidth: "40rem" }}>
              <PliegoCampo etiqueta={`Glosario de ${s.nombre}, separado por comas`} value={glosario.texto} autoFocus
                placeholder="Javier Tebas, midudev, ElevenLabs" error={glosario.error || undefined}
                onChange={(e) => setGlosario({ ...glosario, texto: e.target.value, error: "" })} />
              <div className="fila">
                <PliegoBoton type="submit" size="sm" tone="rosa">guardar</PliegoBoton>
                <PliegoBoton type="button" size="sm" variant="outline" onClick={() => setGlosario(null)}>cancelar</PliegoBoton>
              </div>
            </form>
          ) : (
            <div key={s.id} className="fila">
              <strong>{s.nombre}</strong>
              <span style={{ fontSize: ".95rem" }}>{s.glosario || "sin glosario"}</span>
              <button type="button" className="micro" style={enlace}
                onClick={() => setGlosario({ sala: s.id, texto: s.glosario ?? "", error: "" })}>editar</button>
            </div>
          )
        ))}
      </section>

      {cambio && (
        <PliegoAviso
          pregunta={`¿Pasar «${cambio.sala.nombre}» a ${cambio.backend}?`}
          detalle={cambio.backend === "gemini"
            ? "Gemini cobra por tramo (≈ 0,06 USD por sala-hora medido con Flash-Lite). Vale desde el próximo tramo."
            : cambio.backend === "cpu"
              ? "En CPU el subtítulo llega con más atraso (medido: ~6 s de proceso por tramo con 6 salas). Vale desde el próximo tramo."
              : "Vuelve a la GPU de la casa. Vale desde el próximo tramo."}
          confirmar="cambiar" cancelar="dejarlo" onConfirmar={aplicarCambio} onCancelar={() => setCambio(null)} />
      )}
    </div>
  );
}
