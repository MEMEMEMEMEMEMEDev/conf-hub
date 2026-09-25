// La lógica de la consola de emisión, sin navegador: qué hacer ante cada
// corte. Existe porque el esquema real de un escenario de Nerdearla es una
// mini PC al lado de la placa de audio, con alguien entrando por escritorio
// remoto a apretar F5 si algo se traba. El objetivo es que nadie tenga que
// entrar: la consola se recupera sola de todo lo que se puede recuperar, y
// grita lo que no (un cable desenchufado).

/** Espera antes del reintento n (0, 1, 2…): 0,5 s → 1 → 2 → 4 → 5 s tope. */
export function espera(n: number): number {
  return Math.min(500 * 2 ** Math.max(0, n), 5000);
}

/** El hub manda un mensaje por segundo; sin nada en 5 s, la conexión está
 *  muerta aunque el navegador no se haya enterado (un wifi que se cae sin
 *  cerrar el socket). */
export const SIN_LATIDO_MS = 5000;
export function conexionMuerta(ultimoMensaje: number, ahora: number): boolean {
  return ahora - ultimoMensaje > SIN_LATIDO_MS;
}

/** Nivel por debajo del cual es silencio (dBFS) y cuánto silencio alarma. */
export const SILENCIO_DB = -70;
export const SILENCIO_ALARMA_MS = 60_000;

/** Lleva cuánto hace que no hay sonido. `nivel` llega cada 100 ms. */
export class Silencio {
  private desde: number | null = null;
  registrar(db: number, ahora: number) {
    if (db > SILENCIO_DB) this.desde = null;
    else if (this.desde === null) this.desde = ahora;
  }
  /** ms de silencio seguido (0 si hay sonido). */
  hace(ahora: number): number {
    return this.desde === null ? 0 : ahora - this.desde;
  }
  alarma(ahora: number): boolean {
    return this.hace(ahora) >= SILENCIO_ALARMA_MS;
  }
}

/** Lo que se recuerda de una sala para volver a emitir sola tras un F5 o
 *  un reinicio: sólo la fuente (el token viaja en el fragmento de la URL). */
export type FuenteGuardada = "microfono" | null;
const clave = (sala: string) => `conf:emitir:${sala}`;
export function recordar(sala: string, fuente: FuenteGuardada, almacen: Pick<Storage, "setItem" | "removeItem"> = localStorage) {
  try {
    if (fuente) almacen.setItem(clave(sala), fuente);
    else almacen.removeItem(clave(sala));
  } catch {
    /* modo privado: no se recuerda, y listo */
  }
}
export function recordada(sala: string, almacen: Pick<Storage, "getItem"> = localStorage): FuenteGuardada {
  try {
    return almacen.getItem(clave(sala)) === "microfono" ? "microfono" : null;
  } catch {
    return null;
  }
}

export function duracion(ms: number): string {
  const s = Math.floor(ms / 1000);
  const h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), r = s % 60;
  return h ? `${h} h ${m} min` : m ? `${m} min ${r} s` : `${r} s`;
}
