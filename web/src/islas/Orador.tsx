import { useEffect, useRef, useState } from "react";
import { PliegoBoton, PliegoEnVivo, PliegoEstado, PliegoFilete, PliegoPregunta, PliegoTitulo } from "@ahroi/foundation/pliego";
import { escuchar, hace, leerParametros, moderar, preguntas as pedirPreguntas } from "../lib/sala";
import type { EstadoSala, PreguntaT, Reaccion, Sala } from "../lib/sala";

// La vista del orador: /orador/?s=<sala>. Para la notebook del escenario o
// el monitor de retorno. Lo que el orador necesita y nada más: las
// preguntas que la sala votó, la más votada arriba, en letra que se lee a
// dos metros; y cómo está reaccionando la sala en el último minuto.
const VENTANA = 60_000;
const GLIFO: Record<string, string> = { aplauso: "👏", fuego: "🔥", duda: "?" };

export default function Orador() {
  const [{ sala: id }] = useState(() => leerParametros(location.search));
  const [sala, setSala] = useState<Sala | null>(null);
  const [lista, setLista] = useState<PreguntaT[]>([]);
  const [estado, setEstado] = useState<EstadoSala | null>(null);
  const [conectado, setConectado] = useState(false);
  const [operador, setOperador] = useState(false);
  const [ahora, setAhora] = useState(() => Date.now());
  const recientes = useRef<{ t: number; id: string; n: number }[]>([]);
  const [termometro, setTermometro] = useState<Record<string, number>>({});

  useEffect(() => {
    if (!id) return;
    fetch(`/api/salas/${id}`).then((r) => (r.ok ? r.json() : null)).then(setSala).catch(() => {});
    fetch("/api/sesion").then((r) => setOperador(r.ok)).catch(() => {});
    pedirPreguntas(id).then((ps) => setLista(ps.filter((p) => p.estado !== "pendiente")));
    const cerrar = escuchar(id, {
      subtitulo: () => {},
      estado: setEstado,
      conexion: setConectado,
      preguntas: setLista,
      reacciones: (r) => {
        const t = Date.now();
        for (const [k, n] of Object.entries(r)) if (n) recientes.current.push({ t, id: k, n });
      },
    });
    const reloj = setInterval(() => {
      const t = Date.now();
      recientes.current = recientes.current.filter((x) => t - x.t < VENTANA);
      const c: Record<string, number> = {};
      for (const x of recientes.current) c[x.id] = (c[x.id] ?? 0) + x.n;
      setTermometro(c);
      setAhora(t);
    }, 1000);
    return () => { cerrar(); clearInterval(reloj); };
  }, [id]);

  if (!id) return <div className="lamina"><PliegoTitulo size="seccion" as="h1">falta la sala</PliegoTitulo></div>;
  const publicadas = lista.filter((p) => p.estado === "publicada");
  const respondidas = lista.filter((p) => p.estado === "respondida");
  return (
    <div className="lamina orador" style={{ gap: "1.5rem" }}>
      <header style={{ display: "grid", gap: ".6rem" }}>
        <PliegoTitulo size="seccion" as="h1" sobre="vista del orador" bang={false}>{(sala?.nombre ?? id).toLowerCase()}</PliegoTitulo>
        <div className="fila">
          <PliegoEnVivo estado={!conectado ? "sinver" : estado?.en_vivo ? "vivo" : "quieto"} grabado={sala?.demo} />
          <span className="micro">{estado ? `${estado.espectadores} mirando los subtítulos` : "…"}</span>
        </div>
      </header>

      <section aria-label="La sala en el último minuto" className="fila" style={{ gap: "1rem 2rem" }}>
        {(["aplauso", "fuego", "duda"] as Reaccion[]).map((k) => (
          <span key={k} style={{ display: "inline-flex", alignItems: "baseline", gap: ".5rem" }}>
            <span style={{ fontSize: "2rem" }} aria-hidden="true">{GLIFO[k]}</span>
            <span style={{ fontSize: "2.2rem", fontWeight: 800, fontVariantNumeric: "tabular-nums" }}>{termometro[k] ?? 0}</span>
            <span className="micro">{k === "duda" ? "dudas" : k === "fuego" ? "fuego" : "aplausos"} · último minuto</span>
          </span>
        ))}
      </section>

      <PliegoFilete label="preguntas de la sala" readout={`${publicadas.length} por responder`} />
      {publicadas.length === 0 && <PliegoEstado estado="bien">Todavía no hay preguntas publicadas.</PliegoEstado>}
      <div>
        {publicadas.map((p, i) => (
          <PliegoPregunta key={p.id} texto={p.texto} votos={p.votos} cuando={hace(ahora - p.creada)}
            className={i === 0 ? "orador__primera" : undefined}
            acciones={operador ? (
              <PliegoBoton size="sm" onClick={() => moderar(id, p.id, "respondida")}>ya la respondí</PliegoBoton>
            ) : undefined} />
        ))}
      </div>
      {respondidas.length > 0 && (
        <details>
          <summary className="micro" style={{ cursor: "pointer" }}>respondidas ({respondidas.length})</summary>
          {respondidas.map((p) => <PliegoPregunta key={p.id} texto={p.texto} votos={p.votos} estado="respondida" />)}
        </details>
      )}
    </div>
  );
}
