import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import {
  PliegoBoton, PliegoCampo, PliegoEstado, PliegoFilete, PliegoPregunta, PliegoReacciones,
} from "@ahroi/foundation/pliego";
import type { PliegoReaccionFlotante } from "@ahroi/foundation/pliego";
import { hace, mezclarPreguntas, preguntar, preguntas as pedirPreguntas, reaccionar, sugerir, votar } from "../lib/sala";
import type { PreguntaT, Reaccion } from "../lib/sala";

export const OPCIONES: { id: Reaccion; glifo: string; label: string }[] = [
  { id: "aplauso", glifo: "👏", label: "aplauso" },
  { id: "fuego", glifo: "🔥", label: "fuego" },
  { id: "duda", glifo: "?", label: "tengo una duda" },
  { id: "noseentiende", glifo: "!", label: "no se entiende" },
];
const GLIFO = Object.fromEntries(OPCIONES.map((o) => [o.id, o.glifo]));

export interface InteraccionesApi {
  /** Lo llama la sala con cada evento `reacciones` del SSE. */
  llegaron: (r: Partial<Record<Reaccion, number>>) => void;
  /** Lo llama la sala con cada evento `preguntas` del SSE. */
  publicas: (p: PreguntaT[]) => void;
}

// Las interacciones de una sala. Los eventos llegan por el SSE que ya
// abrió la sala (no se abre otra conexión): la sala le pasa lo que recibe
// por `registrar`.
export default function Interacciones({ sala, registrar }: { sala: string; registrar: (api: InteraccionesApi) => void }) {
  const [flotando, setFlotando] = useState<PliegoReaccionFlotante[]>([]);
  const [aviso, setAviso] = useState("");
  const [enPausa, setEnPausa] = useState(false);
  const [publicas, setPublicas] = useState<PreguntaT[]>([]);
  const [mias, setMias] = useState<PreguntaT[]>([]);
  const [votadas, setVotadas] = useState<Set<string>>(new Set());
  const [texto, setTexto] = useState("");
  const [errorPregunta, setErrorPregunta] = useState("");
  const [termino, setTermino] = useState("");
  const [sugerido, setSugerido] = useState("");
  const n = useRef(0);
  const [ahora, setAhora] = useState(() => Date.now());

  const flotar = useCallback((glifo: string, cuantas: number) => {
    // Hasta 6 por lote: 200 aplausos en un segundo no son 200 signos.
    const nuevas = Array.from({ length: Math.min(cuantas, 6) }, () => ({
      key: `f${n.current++}`, glifo, x: Math.random(),
    }));
    setFlotando((f) => [...f.slice(-24), ...nuevas]);
    setTimeout(() => setFlotando((f) => f.filter((x) => !nuevas.some((y) => y.key === x.key))), 1900);
  }, []);

  useEffect(() => {
    registrar({
      llegaron: (r) => {
        for (const [id, c] of Object.entries(r)) if (c && GLIFO[id]) flotar(GLIFO[id], c);
      },
      publicas: setPublicas,
    });
    pedirPreguntas(sala).then((ps) => {
      setPublicas(ps.filter((p) => p.estado !== "pendiente"));
      setMias(ps.filter((p) => p.mia));
      setVotadas(new Set(ps.filter((p) => p.votada).map((p) => p.id)));
    });
    const t = setInterval(() => setAhora(Date.now()), 30_000);
    return () => clearInterval(t);
  }, [sala, registrar, flotar]);

  async function tocar(id: string) {
    const r = await reaccionar(sala, id as Reaccion);
    if (r.status === 429) {
      setEnPausa(true);
      setTimeout(() => setEnPausa(false), 3000);
      return;
    }
    if (id === "noseentiende") {
      setAviso("Gracias: producción ya lo está viendo.");
      setTimeout(() => setAviso(""), 5000);
    }
  }

  async function enviarPregunta(e: FormEvent) {
    e.preventDefault();
    setErrorPregunta("");
    const r = await preguntar(sala, texto);
    if (!r.ok) return setErrorPregunta(String(r.datos.mensaje ?? `Error ${r.status}`));
    setMias((m) => [r.datos as unknown as PreguntaT, ...m]);
    setTexto("");
  }

  async function alVotar(p: PreguntaT) {
    const r = await votar(sala, p.id);
    if (!r.ok) return;
    setVotadas((v) => {
      const x = new Set(v);
      if (r.datos.votada) x.add(p.id);
      else x.delete(p.id);
      return x;
    });
    setPublicas((ps) => ps.map((q) => (q.id === p.id ? { ...q, votos: Number(r.datos.votos) } : q)));
  }

  async function enviarSugerencia(e: FormEvent) {
    e.preventDefault();
    const r = await sugerir(sala, termino);
    setSugerido(r.ok ? `Recibido: «${termino}». Si producción lo aprueba, entra al glosario de la sala.`
      : String(r.datos.mensaje ?? `Error ${r.status}`));
    if (r.ok) setTermino("");
  }

  const lista = mezclarPreguntas(publicas, mias, votadas);
  return (
    <section aria-label="Participar" style={{ display: "grid", gap: "1.25rem" }}>
      <div style={{ display: "grid", gap: ".5rem", paddingTop: "3rem" }}>
        <PliegoReacciones opciones={OPCIONES} onReaccionar={tocar} flotando={flotando} enPausa={enPausa} />
        {aviso && <PliegoEstado estado="bien">{aviso}</PliegoEstado>}
        {enPausa && <PliegoEstado estado="aviso">Muy seguido: esperá unos segundos.</PliegoEstado>}
      </div>

      <PliegoFilete label="preguntas al orador" readout={lista.length ? String(lista.length) : undefined} />
      <form onSubmit={enviarPregunta} style={{ display: "grid", gap: ".6rem" }}>
        <PliegoCampo etiqueta="Tu pregunta" value={texto} maxLength={280} placeholder="¿…?"
          onChange={(e) => setTexto(e.target.value)} error={errorPregunta || undefined}
          ayuda="Anónima. Producción la revisa antes de mostrarla a la sala." />
        <div><PliegoBoton type="submit" size="sm" tone="rosa" cursor disabled={texto.trim().length < 5}>preguntar</PliegoBoton></div>
      </form>
      <div>
        {lista.length === 0 && <p className="prosa">Todavía no hay preguntas publicadas.</p>}
        {lista.map((p) => (
          <PliegoPregunta key={p.id} texto={p.texto} votos={p.votos} votada={p.votada} estado={p.estado === "descartada" ? "respondida" : p.estado}
            onVotar={p.estado === "publicada" ? () => alVotar(p) : undefined} cuando={hace(ahora - p.creada)} />
        ))}
      </div>

      <details>
        <summary className="micro" style={{ cursor: "pointer" }}>¿un nombre sale mal escrito?</summary>
        <form onSubmit={enviarSugerencia} style={{ display: "grid", gap: ".6rem", marginTop: ".8rem" }}>
          <PliegoCampo etiqueta="Cómo se escribe" value={termino} maxLength={40} placeholder="Javier Tebas"
            onChange={(e) => setTermino(e.target.value)}
            ayuda="Un nombre propio o una marca, como debería salir. Producción lo agrega al glosario de la sala." />
          <div><PliegoBoton type="submit" size="sm" variant="outline" disabled={termino.trim().length < 2}>sugerir</PliegoBoton></div>
          {sugerido && <p className="prosa" role="status">{sugerido}</p>}
        </form>
      </details>
    </section>
  );
}
