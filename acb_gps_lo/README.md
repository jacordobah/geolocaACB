# ACB_GPS_Lo — incremento 1

Servicio mínimo de salud en Go con Redis. No crea sesiones ni guarda posiciones.
No necesita Python ni MongoDB para arrancar. La integración HTTP geográfica y
`/health/ready?capability=create-session` corresponden al siguiente incremento.

## Requisitos

- Go 1.27.3 para pruebas locales, o Docker con Compose para ejecutar el entorno.
- Docker Engine operativo y Compose compatible con `depends_on: service_healthy`.

## Arranque aislado

Desde este directorio, `docker compose -f compose.yaml up --build -d` construye
el binario Go y levanta únicamente los servicios `gps` y `redis` del proyecto
`acb-gps-lo`. Los valores de `.env.example` pueden copiarse a un archivo `.env`
local, sin secretos. No se necesita ese archivo para los valores predeterminados.
El API queda en `http://127.0.0.1:8081` por defecto; Redis sólo está disponible
en la red privada del proyecto. `docker compose -f compose.yaml down` detiene
ambos servicios y conserva el volumen Redis; no usar `down -v` si se quiere
preservar su contenido.

Las imágenes de compilación Go 1.27.3, ejecución Distroless Debian 13 y Redis
8.2.9 están fijadas por digest. El módulo Go fija sus dependencias en `go.mod`
y `go.sum`; la compilación desactiva CGO y elimina las rutas locales del binario.

## Configuración

| Variable | Predeterminado | Función |
| --- | --- | --- |
| `GPS_HTTP_PORT` | `8080` | Puerto dentro del contenedor |
| `GPS_HOST_PORT` | `8081` | Puerto publicado sólo en localhost por Compose |
| `GPS_REDIS_ADDR` | `redis:6379` | Dirección Redis en la red Compose |
| `GPS_REDIS_DIAL_TIMEOUT` | `2s` | Límite para conexión a Redis |
| `GPS_REDIS_PING_TIMEOUT` | `1s` | Límite de la comprobación de disponibilidad |
| `GPS_SHUTDOWN_TIMEOUT` | `20s` | Plazo para terminar solicitudes HTTP activas |
| `REDIS_MAXMEMORY` | `512mb` | Límite inicial sólo para pruebas de arranque |
| `REDIS_AOF_FSYNC` | `always` | Sincronización AOF por escritura |

El puerto interno que elija `GPS_HTTP_PORT` debe coincidir con la segunda parte
del mapeo en Compose. Redis usa AOF y `noeviction`. `appendfsync always` favorece
la durabilidad, pero su coste de E/S se debe medir. `512mb` **no es una capacidad
validada** para posiciones GPS; antes de recibir seguimiento se dimensionarán
memoria, disco y volumen AOF mediante pruebas de carga. Una pérdida del volumen
no está cubierta por esta configuración.

## Salud

- `GET /health/live`: HTTP 200 si el proceso atiende solicitudes; no consulta Redis.
- `GET /health/ready`: HTTP 200 si Redis responde a PING dentro del plazo, o
  HTTP 503 si falla. Ambas respuestas son JSON y no revelan credenciales.

El healthcheck de Compose ejecuta `/acb-gps healthcheck`, que consulta el endpoint
local de readiness. Go no sale al perder Redis: liveness sigue disponible y
readiness cambia a 503. El binario maneja `SIGTERM` y espera las solicitudes en
curso hasta el plazo configurado. La imagen final Distroless ejecuta el binario
sin shell y como usuario no privilegiado.

Las URL del servicio geográfico Python, Operaciones y las políticas GPS no se
requieren ni se usan todavía. Ningún archivo de este componente contiene
credenciales reales.
