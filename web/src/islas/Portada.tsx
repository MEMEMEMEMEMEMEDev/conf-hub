import { useEffect, useState } from "react";
import { PliegoEstado, PliegoEtiqueta, PliegoMenu, PliegoTicker } from "@ahroi/foundation/pliego";
import type { PliegoMenuItem } from "@ahroi/foundation/pliego";
import { NOMBRE_IDIOMA, salas as pedirSalas } from "../lib/sala";
import type { Sala } from "../lib/sala";

// La lista de salas de la portada. Se pide al cargar y cada 60 s: nada de
// polling rápido, que el borde cuenta a todo internet como un solo cliente
// (guía 08 §5).
export default function Portada() {
  const [salas, setSalas] = useState<Sala[] | null>(null);
  const [error, setError] = useState(false);
  const [cursor, setCursor] = useState<string>();

  useEffect(() => {
    let vivo = true;
    const cargar = () =>
      pedirSalas()
        .then((s) => vivo && (setSalas(s), setError(false)))
        .catch(() => vivo && setError(true));
    cargar();
    const t = setInterval(cargar, 60_000);
    return () => {
      vivo = false;
      clearInterval(t);
    };
  }, []);

  if (error && !salas) {
    return <PliegoEstado estado="sinver">No pude preguntar qué salas hay. Reintento en un minuto.</PliegoEstado>;
  }
  if (!salas) return <p className="micro">Buscando salas…</p>;
  if (salas.length === 0) return <p className="prosa">No hay salas abiertas ahora mismo.</p>;

  const destino = (s: Sala) => (s.idioma === "en" ? "es" : "en");
  const items: PliegoMenuItem[] = salas.map((s) => ({
    id: s.id,
    label: s.nombre.toLowerCase(),
    meta: [
      s.demo ? "demo · audio grabado" : s.en_vivo ? "en vivo" : "sin audio ahora",
      `${NOMBRE_IDIOMA[s.idioma]} → ${NOMBRE_IDIOMA[destino(s)]}`,
      s.espectadores > 0 ? `${s.espectadores} mirando` : "",
    ].filter(Boolean).join(" · "),
    href: `/sala/?s=${s.id}&l=${destino(s)}`,
  }));
  const hayDemo = salas.some((s) => s.demo);
  const cinta = salas.map((s) => ({
    id: s.id,
    contenido: (
      <>
        {s.nombre.toLowerCase()}{" "}
        <PliegoEtiqueta tone={s.demo ? "tinta" : s.en_vivo ? "rosa" : "linea"} size="sm">
          {s.demo ? "grabado" : s.en_vivo ? "en vivo" : "sin audio"}
        </PliegoEtiqueta>{" "}
        {NOMBRE_IDIOMA[s.idioma]} → {NOMBRE_IDIOMA[destino(s)]}
        {s.espectadores > 0 ? ` · ${s.espectadores} mirando` : ""}
      </>
    ),
  }));
  return (
    <div style={{ display: "grid", gap: "1rem" }}>
      <PliegoTicker label="Estado de las salas" items={cinta} vuelta={Math.max(24, salas.length * 9)} />
      <PliegoMenu label="Elegí una sala" items={items} numerado value={cursor} onChange={setCursor} />
      {hayDemo && (
        <p className="prosa">
          <PliegoEtiqueta tone="rosa" size="sm">demo</PliegoEtiqueta>{" "}
          Las salas demo reproducen en bucle fragmentos de charlas reales de Nerdearla 2025 (con licencia
          Apache 2.0, del repositorio de Subtitula). El audio es grabado; la transcripción y la traducción se
          hacen en vivo, en este momento, cuando alguien abre la sala.
        </p>
      )}
    </div>
  );
}
